package policy

import (
	"net/netip"
	"testing"
)

func TestMatcherDomainSpecificity(t *testing.T) {
	matcher := NewMatcher([]Rule{
		{ID: "bare", Kind: "domain", Value: "example.com"},
		{ID: "wild", Kind: "domain", Value: "*.api.example.com"},
	})
	cases := []struct {
		host string
		want string
	}{
		{"example.com", "bare"},
		{"www.example.com", "bare"},
		{"x.api.example.com", "wild"},
		{"api.example.com", "bare"},
		{"notexample.com", ""},
	}
	for _, tc := range cases {
		rule, ok := matcher.MatchDomain(tc.host)
		if tc.want == "" {
			if ok {
				t.Fatalf("%s: unexpected rule %s", tc.host, rule.ID)
			}
			continue
		}
		if !ok || rule.ID != tc.want {
			t.Fatalf("%s: got %q ok=%v want %q", tc.host, rule.ID, ok, tc.want)
		}
	}
}

func TestMatcherIPSpecificity(t *testing.T) {
	matcher := NewMatcher([]Rule{
		{ID: "cidr8", Kind: "cidr", Value: "10.0.0.0/8"},
		{ID: "cidr24", Kind: "cidr", Value: "10.1.2.0/24"},
		{ID: "exact", Kind: "ip", Value: "10.1.2.3"},
	})
	cases := []struct {
		ip   string
		want string
	}{
		{"10.1.2.3", "exact"},
		{"10.1.2.99", "cidr24"},
		{"10.9.9.9", "cidr8"},
		{"192.168.1.1", ""},
	}
	for _, tc := range cases {
		rule, ok := matcher.MatchIP(netip.MustParseAddr(tc.ip))
		if tc.want == "" {
			if ok {
				t.Fatalf("%s: unexpected rule %s", tc.ip, rule.ID)
			}
			continue
		}
		if !ok || rule.ID != tc.want {
			t.Fatalf("%s: got %q ok=%v want %q", tc.ip, rule.ID, ok, tc.want)
		}
	}
}

func TestMatcherIPv6(t *testing.T) {
	matcher := NewMatcher([]Rule{{ID: "v6", Kind: "cidr", Value: "2001:db8::/32"}})
	if _, ok := matcher.MatchIP(netip.MustParseAddr("2001:db8::1")); !ok {
		t.Fatal("expected IPv6 match")
	}
	if _, ok := matcher.MatchIP(netip.MustParseAddr("2001:db9::1")); ok {
		t.Fatal("unexpected IPv6 match")
	}
}
