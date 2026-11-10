package policy

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

// normalizeHostname lower-cases and strips a trailing dot.
func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// Matcher is an immutable rule index optimized for packet-path lookups.
type Matcher struct {
	ips       map[netip.Addr]Rule
	cidrs     []cidrRule
	exact     map[string]Rule
	wildcards map[string]Rule
}

type cidrRule struct {
	prefix netip.Prefix
	rule   Rule
}

// NewMatcher indexes validated rules.
func NewMatcher(rules []Rule) *Matcher {
	m := &Matcher{
		ips:       map[netip.Addr]Rule{},
		exact:     map[string]Rule{},
		wildcards: map[string]Rule{},
	}
	for _, rule := range rules {
		switch rule.Kind {
		case "ip":
			addr, err := ruleutil.NormalizeIP(rule.Value)
			if err != nil {
				continue
			}
			m.ips[addr] = rule
		case "cidr":
			prefix, err := ruleutil.NormalizeCIDR(rule.Value)
			if err != nil {
				continue
			}
			m.cidrs = append(m.cidrs, cidrRule{prefix: prefix, rule: rule})
		case "domain":
			value, err := ruleutil.NormalizeDomain(rule.Value)
			if err != nil {
				continue
			}
			if strings.HasPrefix(value, "*.") {
				m.wildcards[strings.TrimPrefix(value, "*.")] = rule
			} else {
				m.exact[value] = rule
			}
		case "asn":
			for _, rawPrefix := range rule.Prefixes {
				prefix, err := ruleutil.NormalizeCIDR(rawPrefix)
				if err != nil {
					continue
				}
				m.cidrs = append(m.cidrs, cidrRule{prefix: prefix, rule: rule})
			}
		}
	}
	sort.Slice(m.cidrs, func(i, j int) bool {
		if m.cidrs[i].prefix.Bits() != m.cidrs[j].prefix.Bits() {
			return m.cidrs[i].prefix.Bits() > m.cidrs[j].prefix.Bits()
		}
		return m.cidrs[i].prefix.Addr().Compare(m.cidrs[j].prefix.Addr()) < 0
	})
	return m
}

// MatchIP resolves an IP rule.
func (m *Matcher) MatchIP(addr netip.Addr) (Rule, bool) {
	if m == nil {
		return Rule{}, false
	}
	addr = addr.Unmap()
	if rule, ok := m.ips[addr]; ok {
		return rule, true
	}
	for _, candidate := range m.cidrs {
		if candidate.prefix.Addr().Is4() != addr.Is4() {
			continue
		}
		if candidate.prefix.Contains(addr) {
			return candidate.rule, true
		}
	}
	return Rule{}, false
}

// MatchDomain resolves a domain rule. The longest matching suffix wins.
func (m *Matcher) MatchDomain(host string) (Rule, bool) {
	if m == nil {
		return Rule{}, false
	}
	name := normalizeHostname(host)
	if name == "" {
		return Rule{}, false
	}
	for i, suffix := range ruleutil.DomainSuffixes(name) {
		if rule, ok := m.exact[suffix]; ok {
			return rule, true
		}
		if i > 0 {
			if rule, ok := m.wildcards[suffix]; ok {
				return rule, true
			}
		}
	}
	return Rule{}, false
}

// Count returns the number of indexed rules.
func (m *Matcher) Count() int {
	if m == nil {
		return 0
	}
	return len(m.ips) + len(m.cidrs) + len(m.exact) + len(m.wildcards)
}
