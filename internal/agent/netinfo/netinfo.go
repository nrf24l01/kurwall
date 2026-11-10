// Package netinfo tracks local interface addresses so the flow tracker can
// distinguish outgoing host traffic from forwarded inbound traffic.
package netinfo

import (
	"net"
	"net/netip"
	"sync"
)

// Local is a refreshable snapshot of local addresses.
type Local struct {
	mu    sync.RWMutex
	addrs map[netip.Addr]struct{}
}

// NewLocal builds and populates a local address set.
func NewLocal() *Local {
	l := &Local{addrs: map[netip.Addr]struct{}{}}
	_ = l.Refresh()
	return l
}

// Refresh reloads interface addresses.
func (l *Local) Refresh() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	next := map[netip.Addr]struct{}{}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err != nil {
				continue
			}
			next[prefix.Addr().Unmap()] = struct{}{}
		}
	}
	l.mu.Lock()
	l.addrs = next
	l.mu.Unlock()
	return nil
}

// Contains reports whether addr belongs to a local interface.
func (l *Local) Contains(addr netip.Addr) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.addrs[addr.Unmap()]
	return ok
}
