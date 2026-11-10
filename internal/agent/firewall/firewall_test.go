package firewall

import (
	"net/netip"
	"testing"
)

func TestBuildElementsMergesOverlaps(t *testing.T) {
	elements := buildElements([]prefixRule{
		{prefix: netip.MustParsePrefix("10.0.0.0/8")},
		{prefix: netip.MustParsePrefix("10.1.2.0/24")},
		{prefix: netip.MustParsePrefix("192.168.0.0/16")},
	})
	if len(elements) != 4 {
		t.Fatalf("expected 4 interval elements, got %d", len(elements))
	}
	// 10.0.0.0/8 is emitted as start + end marker.
	if got := netip.AddrFrom4([4]byte(elements[0].Key)); got.String() != "10.0.0.0" {
		t.Fatalf("unexpected start: %s", got)
	}
	if !elements[1].IntervalEnd {
		t.Fatal("expected interval end marker")
	}
	if got := netip.AddrFrom4([4]byte(elements[1].Key)); got.String() != "11.0.0.0" {
		t.Fatalf("unexpected end marker: %s", got)
	}
}

func TestBuildElementsIPv6(t *testing.T) {
	elements := buildElements([]prefixRule{{prefix: netip.MustParsePrefix("2001:db8::/32")}})
	if len(elements) != 2 {
		t.Fatalf("expected start and end marker, got %d", len(elements))
	}
	if !elements[1].IntervalEnd {
		t.Fatal("expected IPv6 interval end marker")
	}
}

func TestLastAddr(t *testing.T) {
	cases := map[string]string{
		"10.0.0.0/8":     "10.255.255.255",
		"192.168.1.0/24": "192.168.1.255",
		"192.168.1.1/32": "192.168.1.1",
		"2001:db8::/32":  "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff",
	}
	for prefix, want := range cases {
		if got := lastAddr(netip.MustParsePrefix(prefix)).String(); got != want {
			t.Fatalf("%s: got %s want %s", prefix, got, want)
		}
	}
}

func TestNextAddr(t *testing.T) {
	next, ok := nextAddr(netip.MustParseAddr("255.255.255.255"))
	if ok {
		t.Fatalf("expected overflow, got %s", next)
	}
	next, ok = nextAddr(netip.MustParseAddr("10.0.0.9"))
	if !ok || next.String() != "10.0.0.10" {
		t.Fatalf("got %s ok=%v", next, ok)
	}
}
