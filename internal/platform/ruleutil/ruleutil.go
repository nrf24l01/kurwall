// Package ruleutil normalizes and matches Kurwall filtering rules. The same
// implementation is used by the coordinator (validation) and the agent
// (enforcement) so both agree on semantics.
package ruleutil

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// NormalizeIP canonicalizes a single IP rule value.
func NormalizeIP(value string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("must be a valid IPv4 or IPv6 address")
	}
	return addr.Unmap(), nil
}

// NormalizeCIDR canonicalizes a CIDR rule value and zeroes host bits.
func NormalizeCIDR(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "/") {
		return netip.Prefix{}, fmt.Errorf("must be a CIDR range such as 10.0.0.0/8")
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("must be a valid IPv4 or IPv6 CIDR range")
	}
	prefix = prefix.Masked()
	if prefix.Bits() == 0 {
		return netip.Prefix{}, fmt.Errorf("must not cover the entire address space")
	}
	return prefix, nil
}

// NormalizeDomain canonicalizes a domain rule or observed hostname.
func NormalizeDomain(value string) (string, error) {
	v := strings.TrimSpace(strings.ToLower(value))
	v = strings.TrimSuffix(v, ".")
	wildcard := strings.HasPrefix(v, "*.")
	if wildcard {
		v = strings.TrimPrefix(v, "*.")
	}
	if v == "" {
		return "", fmt.Errorf("must be a domain name")
	}
	ascii, err := idna.Lookup.ToASCII(v)
	if err != nil {
		return "", fmt.Errorf("must be a valid domain name")
	}
	if err := validateHostname(ascii, wildcard); err != nil {
		return "", err
	}
	if wildcard {
		return "*." + ascii, nil
	}
	return ascii, nil
}

func validateHostname(name string, wildcard bool) error {
	if len(name) > 253 {
		return fmt.Errorf("domain name is too long")
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return fmt.Errorf("must contain at least two labels, for example example.com")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("must be a valid domain name")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("must be a valid domain name")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("must be a valid domain name")
		}
	}
	if wildcard && len(labels) < 2 {
		return fmt.Errorf("wildcard domain must contain a registrable name")
	}
	return nil
}

// NormalizeASN canonicalizes an autonomous system number as AS<number>.
func NormalizeASN(value string) (string, error) {
	asn, err := ParseASN(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("AS%d", asn), nil
}

// ParseASN accepts AS15169, as15169 or 15169 and returns the number.
func ParseASN(value string) (int64, error) {
	v := strings.TrimSpace(value)
	v = strings.TrimPrefix(strings.ToUpper(v), "AS")
	if v == "" {
		return 0, fmt.Errorf("must be an autonomous system number such as AS15169")
	}
	asn, err := strconv.ParseUint(v, 10, 32)
	if err != nil || asn == 0 {
		return 0, fmt.Errorf("must be an autonomous system number between 1 and 4294967295")
	}
	return int64(asn), nil
}

// NormalizeRule validates a rule input and returns its canonical value.
func NormalizeRule(kind, value string) (string, error) {
	switch kind {
	case "ip":
		addr, err := NormalizeIP(value)
		if err != nil {
			return "", err
		}
		return addr.String(), nil
	case "cidr":
		prefix, err := NormalizeCIDR(value)
		if err != nil {
			return "", err
		}
		return prefix.String(), nil
	case "domain":
		return NormalizeDomain(value)
	case "asn":
		return NormalizeASN(value)
	default:
		return "", fmt.Errorf("must be one of ip, cidr, domain or asn")
	}
}

// DomainSuffixes returns every suffix of a normalized hostname, longest first.
// "api.example.com" yields api.example.com, example.com, com.
func DomainSuffixes(name string) []string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		return nil
	}
	labels := strings.Split(name, ".")
	out := make([]string, 0, len(labels))
	for i := range labels {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// MatchDomain reports whether pattern matches hostname. A bare pattern matches
// the domain and all of its subdomains. A "*.domain" pattern matches only
// subdomains.
func MatchDomain(pattern, hostname string) bool {
	p := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	if p == "" || h == "" {
		return false
	}
	if strings.HasPrefix(p, "*.") {
		base := strings.TrimPrefix(p, "*.")
		return strings.HasSuffix(h, "."+base) && h != base
	}
	return h == p || strings.HasSuffix(h, "."+p)
}
