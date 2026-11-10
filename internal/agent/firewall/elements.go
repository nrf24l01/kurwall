package firewall

import (
	"net/netip"
	"sort"

	"github.com/google/nftables"

	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

type prefixRule struct {
	prefix netip.Prefix
	order  int
}

func encodeBlocked(rules []Rule) (v4, v6 []nftables.SetElement) {
	var p4, p6 []prefixRule
	for i, rule := range rules {
		switch rule.Kind {
		case "ip":
			addr, err := ruleutil.NormalizeIP(rule.Value)
			if err != nil {
				continue
			}
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			p := netip.PrefixFrom(addr, bits)
			if addr.Is6() {
				p6 = append(p6, prefixRule{p, i})
			} else {
				p4 = append(p4, prefixRule{p, i})
			}
		case "cidr":
			parsed, err := ruleutil.NormalizeCIDR(rule.Value)
			if err != nil {
				continue
			}
			if parsed.Addr().Is6() {
				p6 = append(p6, prefixRule{parsed, i})
			} else {
				p4 = append(p4, prefixRule{parsed, i})
			}
		}
	}
	return buildElements(p4), buildElements(p6)
}

type ipRange struct {
	start netip.Addr
	end   netip.Addr
}

func buildElements(prefixes []prefixRule) []nftables.SetElement {
	sort.Slice(prefixes, func(i, j int) bool {
		a, b := prefixes[i].prefix.Addr(), prefixes[j].prefix.Addr()
		if cmp := a.Compare(b); cmp != 0 {
			return cmp < 0
		}
		return prefixes[i].prefix.Bits() < prefixes[j].prefix.Bits()
	})
	var ranges []ipRange
	for _, item := range prefixes {
		start := item.prefix.Masked().Addr()
		end := lastAddr(item.prefix)
		if len(ranges) > 0 {
			last := &ranges[len(ranges)-1]
			adjacent := false
			if next, ok := nextAddr(last.end); ok {
				adjacent = start.Compare(next) <= 0
			}
			if start.Compare(last.start) >= 0 && (start.Compare(last.end) <= 0 || adjacent) {
				if end.Compare(last.end) > 0 {
					last.end = end
				}
				continue
			}
		}
		ranges = append(ranges, ipRange{start: start, end: end})
	}
	out := make([]nftables.SetElement, 0, len(ranges)*2)
	for _, r := range ranges {
		out = append(out, nftables.SetElement{Key: addrBytes(r.start)})
		if next, ok := nextAddr(r.end); ok {
			out = append(out, nftables.SetElement{Key: addrBytes(next), IntervalEnd: true})
		}
	}
	return out
}
func addrBytes(addr netip.Addr) []byte {
	if addr.Is6() {
		b := addr.As16()
		return b[:]
	}
	b := addr.As4()
	return b[:]
}

func lastAddr(prefix netip.Prefix) netip.Addr {
	addr := prefix.Masked().Addr()
	raw := addr.AsSlice()
	bits := prefix.Bits()
	for i := range raw {
		for j := 0; j < 8; j++ {
			if i*8+j >= bits {
				raw[i] |= 1 << (7 - j)
			}
		}
	}
	out, ok := netip.AddrFromSlice(raw)
	if !ok {
		return addr
	}
	if addr.Is4() {
		return out.Unmap()
	}
	return out
}

func nextAddr(addr netip.Addr) (netip.Addr, bool) {
	raw := addr.AsSlice()
	for i := len(raw) - 1; i >= 0; i-- {
		raw[i]++
		if raw[i] != 0 {
			out, ok := netip.AddrFromSlice(raw)
			if !ok {
				return netip.Addr{}, false
			}
			if addr.Is4() {
				return out.Unmap(), true
			}
			return out, true
		}
	}
	return netip.Addr{}, false
}

// LogPrefix returns the nftables block log prefix used for diagnostics.
