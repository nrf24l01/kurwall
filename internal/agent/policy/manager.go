// Package policy caches and matches the filtering policy distributed by the
// coordinator. It applies changes locally so filtering survives disconnects.
package policy

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/agent/firewall"
	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

// Rule is a normalized filtering rule.
type Rule struct {
	ID         string
	PolicyID   string
	PolicyName string
	Kind       string
	Value      string
	Action     string
	Note       string
	Prefixes   []string
}

// Settings controls local inspection.
type Settings struct {
	TLSPorts      []uint16
	DNSInspection bool
	TLSInspection bool
}

// Snapshot is the agent-side view of a policy revision.
type Snapshot struct {
	Version  int64
	Rules    []Rule
	Settings Settings
}

// Firewall applies packet-level policy.
type Firewall interface {
	Apply(ctx context.Context, rules []firewall.Rule, tlsPorts []uint16, dnsEnabled, tlsEnabled bool) error
}

// Cache persists the last applied snapshot for offline restarts.
type Cache interface {
	SavePolicy(version int64, payload []byte) error
	LoadPolicy() (version int64, payload []byte, found bool, err error)
}

// Manager owns policy state and applies revisions serially.
type Manager struct {
	mu        sync.RWMutex
	fw        Firewall
	cache     Cache
	logger    zerolog.Logger
	matcher   *Matcher
	settings  Settings
	version   int64
	appliedAt time.Time
	defaults  Snapshot
}

// NewManager builds a policy manager with default settings used before the
// first coordinator snapshot arrives.
func NewManager(fw Firewall, cache Cache, logger zerolog.Logger, defaults Snapshot) *Manager {
	return &Manager{
		fw: fw, cache: cache, logger: logger,
		matcher: NewMatcher(nil), settings: defaults.Settings, defaults: defaults,
	}
}

// Restore loads the cached snapshot, or applies defaults when none exists.
func (m *Manager) Restore(ctx context.Context) error {
	version, payload, found, err := m.cache.LoadPolicy()
	if err != nil {
		return fmt.Errorf("load cached policy: %w", err)
	}
	if !found {
		m.logger.Info().Msg("no cached policy; applying empty default policy")
		return m.applyLocal(ctx, m.defaults, false)
	}
	var cached kurwallv1.PolicySnapshot
	if err := proto.Unmarshal(payload, &cached); err != nil {
		m.logger.Warn().Err(err).Msg("cached policy is corrupt; applying empty default policy")
		return m.applyLocal(ctx, m.defaults, false)
	}
	m.logger.Info().Int64("version", version).Msg("restoring cached policy")
	return m.applyProto(ctx, &cached, false, true)
}

// Apply applies a snapshot received from the coordinator.
func (m *Manager) Apply(ctx context.Context, snap *kurwallv1.PolicySnapshot) error {
	return m.applyProto(ctx, snap, true, false)
}

func (m *Manager) applyProto(ctx context.Context, snap *kurwallv1.PolicySnapshot, persist, force bool) error {
	m.mu.RLock()
	current := m.version
	m.mu.RUnlock()
	if !force && snap.GetVersion() > 0 && snap.GetVersion() <= current {
		return nil
	}
	local := Snapshot{Version: snap.GetVersion(), Settings: settingsFromProto(snap.GetSettings())}
	if len(local.Settings.TLSPorts) == 0 {
		local.Settings.TLSPorts = m.defaults.Settings.TLSPorts
	}
	for _, raw := range snap.GetRules() {
		kind := kindFromProto(raw.GetKind())
		if kind == "" || raw.GetAction() != kurwallv1.RuleAction_RULE_ACTION_BLOCK {
			continue
		}
		value, err := ruleutil.NormalizeRule(kind, raw.GetValue())
		if err != nil {
			m.logger.Warn().Err(err).Str("rule_id", raw.GetId()).Str("value", raw.GetValue()).
				Msg("skipping invalid rule from coordinator")
			continue
		}
		var prefixes []string
		if kind == "asn" {
			for _, rawPrefix := range raw.GetPrefixes() {
				prefix, err := ruleutil.NormalizeCIDR(rawPrefix)
				if err != nil {
					continue
				}
				prefixes = append(prefixes, prefix.String())
			}
		}
		local.Rules = append(local.Rules, Rule{
			ID: raw.GetId(), PolicyID: raw.GetPolicyId(), PolicyName: raw.GetPolicyName(),
			Kind: kind, Value: value, Action: "block", Note: raw.GetNote(), Prefixes: prefixes,
		})
	}
	return m.applyLocal(ctx, local, persist)
}

func (m *Manager) applyLocal(ctx context.Context, snap Snapshot, persist bool) error {
	fwRules := make([]firewall.Rule, 0, len(snap.Rules))
	for _, rule := range snap.Rules {
		switch rule.Kind {
		case "ip", "cidr":
			fwRules = append(fwRules, firewall.Rule{ID: rule.ID, Kind: rule.Kind, Value: rule.Value})
		case "asn":
			for _, prefix := range rule.Prefixes {
				fwRules = append(fwRules, firewall.Rule{ID: rule.ID, Kind: "cidr", Value: prefix})
			}
		}
	}
	if err := m.fw.Apply(ctx, fwRules, snap.Settings.TLSPorts, snap.Settings.DNSInspection, snap.Settings.TLSInspection); err != nil {
		return fmt.Errorf("apply firewall policy: %w", err)
	}
	if persist {
		payload, err := proto.Marshal(&kurwallv1.PolicySnapshot{
			Version: snap.Version,
			Rules:   rulesToProto(snap.Rules),
			Settings: &kurwallv1.AgentSettings{
				TlsPorts: portsToProto(snap.Settings.TLSPorts), DnsInspection: snap.Settings.DNSInspection,
				TlsInspection: snap.Settings.TLSInspection,
			},
		})
		if err != nil {
			m.logger.Warn().Err(err).Msg("failed to encode policy for cache")
		} else if err := m.cache.SavePolicy(snap.Version, payload); err != nil {
			m.logger.Warn().Err(err).Msg("failed to persist policy cache")
		}
	}
	matcher := NewMatcher(snap.Rules)
	m.mu.Lock()
	m.matcher = matcher
	m.settings = snap.Settings
	m.version = snap.Version
	m.appliedAt = time.Now().UTC()
	m.mu.Unlock()
	m.logger.Info().Int64("version", snap.Version).Int("rules", len(snap.Rules)).
		Int("firewall_rules", len(fwRules)).Ints("tls_ports", uint16s(snap.Settings.TLSPorts)).
		Bool("dns_inspection", snap.Settings.DNSInspection).Bool("tls_inspection", snap.Settings.TLSInspection).
		Msg("policy applied")
	return nil
}

func settingsFromProto(s *kurwallv1.AgentSettings) Settings {
	if s == nil {
		return Settings{}
	}
	seen := map[uint16]struct{}{}
	var ports []uint16
	for _, port := range s.GetTlsPorts() {
		if port == 0 || port > 65535 {
			continue
		}
		p := uint16(port)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return Settings{TLSPorts: ports, DNSInspection: s.GetDnsInspection(), TLSInspection: s.GetTlsInspection()}
}

func kindFromProto(kind kurwallv1.RuleKind) string {
	switch kind {
	case kurwallv1.RuleKind_RULE_KIND_IP:
		return "ip"
	case kurwallv1.RuleKind_RULE_KIND_CIDR:
		return "cidr"
	case kurwallv1.RuleKind_RULE_KIND_DOMAIN:
		return "domain"
	case kurwallv1.RuleKind_RULE_KIND_ASN:
		return "asn"
	default:
		return ""
	}
}

func rulesToProto(rules []Rule) []*kurwallv1.PolicyRule {
	out := make([]*kurwallv1.PolicyRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, &kurwallv1.PolicyRule{
			Id: r.ID, PolicyId: r.PolicyID, PolicyName: r.PolicyName,
			Kind: kindToProto(r.Kind), Value: r.Value,
			Action: kurwallv1.RuleAction_RULE_ACTION_BLOCK, Note: r.Note, Prefixes: r.Prefixes,
		})
	}
	return out
}

func kindToProto(kind string) kurwallv1.RuleKind {
	switch kind {
	case "ip":
		return kurwallv1.RuleKind_RULE_KIND_IP
	case "cidr":
		return kurwallv1.RuleKind_RULE_KIND_CIDR
	case "domain":
		return kurwallv1.RuleKind_RULE_KIND_DOMAIN
	case "asn":
		return kurwallv1.RuleKind_RULE_KIND_ASN
	default:
		return kurwallv1.RuleKind_RULE_KIND_UNSPECIFIED
	}
}

// MatchIP returns the rule blocking an address, preferring exact IPs and then
// the longest matching CIDR.
func (m *Manager) MatchIP(addr netip.Addr) (Rule, bool) {
	m.mu.RLock()
	matcher := m.matcher
	m.mu.RUnlock()
	return matcher.MatchIP(addr)
}

// MatchDomain returns the rule blocking a hostname.
func (m *Manager) MatchDomain(host string) (Rule, bool) {
	m.mu.RLock()
	matcher := m.matcher
	m.mu.RUnlock()
	return matcher.MatchDomain(host)
}

// Version returns the applied revision.
func (m *Manager) Version() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// AppliedAt returns when the current policy was applied.
func (m *Manager) AppliedAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.appliedAt
}

// Settings returns the active inspection settings.
func (m *Manager) Settings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// RuleCount returns the number of active rules.
func (m *Manager) RuleCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.matcher.Count()
}

func portsToProto(in []uint16) []uint32 {
	out := make([]uint32, len(in))
	for i, v := range in {
		out[i] = uint32(v)
	}
	return out
}

func uint16s(in []uint16) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}
