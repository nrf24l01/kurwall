package ruleutil

import (
	"net/netip"
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"Example.COM.", "example.com", false},
		{"*.Example.com", "*.example.com", false},
		{"münchen.de", "xn--mnchen-3ya.de", false},
		{"localhost", "", true},
		{"bad_domain.com", "", true},
		{"-bad.com", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeDomain(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchDomain(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", true},
		{"example.com", "notexample.com", false},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "example.com", false},
		{"api.example.com", "example.com", false},
	}
	for _, tc := range cases {
		if got := MatchDomain(tc.pattern, tc.name); got != tc.want {
			t.Fatalf("%q vs %q: got %v want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestNormalizeCIDR(t *testing.T) {
	p, err := NormalizeCIDR("10.1.2.3/8")
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != "10.0.0.0/8" {
		t.Fatalf("got %s", p)
	}
	if _, err := NormalizeCIDR("0.0.0.0/0"); err == nil {
		t.Fatal("expected /0 rejection")
	}
	if _, err := NormalizeCIDR("10.0.0.1"); err == nil {
		t.Fatal("expected CIDR parse error")
	}
}

func TestDomainSuffixes(t *testing.T) {
	got := DomainSuffixes("a.b.example.com")
	want := []string{"a.b.example.com", "b.example.com", "example.com", "com"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestCIDRContains(t *testing.T) {
	prefix := netip.MustParsePrefix("10.0.0.0/8")
	if !prefix.Contains(netip.MustParseAddr("10.2.3.4")) {
		t.Fatal("expected contains")
	}
}

func TestNormalizeASN(t *testing.T) {
	cases := map[string]string{
		"15169":   "AS15169",
		"AS15169": "AS15169",
		"as13335": "AS13335",
		" 42 ":    "AS42",
	}
	for input, want := range cases {
		got, err := NormalizeASN(input)
		if err != nil || got != want {
			t.Fatalf("%q: got %q err=%v want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "AS0", "AS99999999999", "ASN1", "AS-1"} {
		if _, err := NormalizeASN(bad); err == nil {
			t.Fatalf("%q: expected an error", bad)
		}
	}
}
