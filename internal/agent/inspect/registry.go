// Package inspect shares connection metadata between the DNS, TLS and flow
// inspectors. It is the agent's in-memory enrichment cache.
package inspect

import (
	"net/netip"
	"sync"
	"time"
)

// Key identifies a connection by its original direction tuple.
type Key struct {
	Src     netip.Addr
	Dst     netip.Addr
	SrcPort uint16
	DstPort uint16
}

// Info is the metadata learned about a connection.
type Info struct {
	SNI         string
	Domain      string
	Blocked     bool
	RuleID      string
	Reason      string
	ContainerID string
	Container   string
	Network     string
	SeenAt      time.Time
}

type domainEntry struct {
	domain  string
	expires time.Time
}

// Registry stores per-connection and IP-to-domain associations with bounded
// memory. Entries are evicted by TTL and by count.
type Registry struct {
	mu         sync.Mutex
	info       map[Key]Info
	ipDomains  map[netip.Addr]domainEntry
	blocked    map[Key]time.Time
	maxEntries int
	infoTTL    time.Duration
	blockedTTL time.Duration
}

// NewRegistry builds a registry with sane bounds for a small server.
func NewRegistry() *Registry {
	return &Registry{
		info:       map[Key]Info{},
		ipDomains:  map[netip.Addr]domainEntry{},
		blocked:    map[Key]time.Time{},
		maxEntries: 100000,
		infoTTL:    10 * time.Minute,
		blockedTTL: 5 * time.Minute,
	}
}

// SetInfo stores metadata for a connection.
func (r *Registry) SetInfo(key Key, info Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info.SeenAt.IsZero() {
		info.SeenAt = time.Now()
	}
	r.info[key] = info
	r.evictInfoLocked()
}

// UpdateInfo merges non-empty fields into existing metadata.
func (r *Registry) UpdateInfo(key Key, update Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.info[key]
	if !ok {
		info.SeenAt = time.Now()
	}
	merge(&info, update)
	r.info[key] = info
	r.evictInfoLocked()
}

func merge(dst *Info, src Info) {
	if src.SNI != "" {
		dst.SNI = src.SNI
	}
	if src.Domain != "" {
		dst.Domain = src.Domain
	}
	if src.Blocked {
		dst.Blocked = true
	}
	if src.RuleID != "" {
		dst.RuleID = src.RuleID
	}
	if src.Reason != "" {
		dst.Reason = src.Reason
	}
	if src.ContainerID != "" {
		dst.ContainerID = src.ContainerID
	}
	if src.Container != "" {
		dst.Container = src.Container
	}
	if src.Network != "" {
		dst.Network = src.Network
	}
	if !src.SeenAt.IsZero() {
		dst.SeenAt = src.SeenAt
	}
}

// Info returns metadata for a connection.
func (r *Registry) Info(key Key) (Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.info[key]
	if !ok {
		return Info{}, false
	}
	if time.Since(info.SeenAt) > r.infoTTL {
		delete(r.info, key)
		return Info{}, false
	}
	return info, true
}

// Forget removes a completed connection.
func (r *Registry) Forget(key Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.info, key)
	delete(r.blocked, key)
}

// RememberDomain associates a resolved IP with a domain.
func (r *Registry) RememberDomain(ip netip.Addr, domain string, ttl time.Duration) {
	if !ip.IsValid() || domain == "" {
		return
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl > 30*time.Minute {
		ttl = 30 * time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ipDomains) >= r.maxEntries {
		r.evictDomainsLocked()
	}
	r.ipDomains[ip.Unmap()] = domainEntry{domain: domain, expires: time.Now().Add(ttl)}
}

// DomainForIP returns the most recently learned domain for an IP.
func (r *Registry) DomainForIP(ip netip.Addr) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.ipDomains[ip.Unmap()]
	if !ok {
		return ""
	}
	if time.Now().After(entry.expires) {
		delete(r.ipDomains, ip.Unmap())
		return ""
	}
	return entry.domain
}

// MarkBlocked remembers a blocked connection so queued retransmissions are
// dropped even before the connmark reaches the kernel.
func (r *Registry) MarkBlocked(key Key, ttl time.Duration) {
	if ttl <= 0 {
		ttl = r.blockedTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocked[key] = time.Now().Add(ttl)
}

// IsBlocked reports whether a connection was already blocked.
func (r *Registry) IsBlocked(key Key) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	expires, ok := r.blocked[key]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(r.blocked, key)
		return false
	}
	return true
}

// Cleanup drops expired entries. Call it periodically.
func (r *Registry) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for key, info := range r.info {
		if now.Sub(info.SeenAt) > r.infoTTL {
			delete(r.info, key)
		}
	}
	for key, expires := range r.blocked {
		if now.After(expires) {
			delete(r.blocked, key)
		}
	}
	for ip, entry := range r.ipDomains {
		if now.After(entry.expires) {
			delete(r.ipDomains, ip)
		}
	}
}

func (r *Registry) evictInfoLocked() {
	if len(r.info) < r.maxEntries {
		return
	}
	now := time.Now()
	removed := 0
	for key, info := range r.info {
		if now.Sub(info.SeenAt) > r.infoTTL {
			delete(r.info, key)
			removed++
		}
	}
	if removed > 0 {
		return
	}
	// Hard bound: drop arbitrary entries. Flows in progress are re-created by
	// later conntrack events, and dropped metadata only degrades enrichment.
	target := len(r.info) - r.maxEntries + r.maxEntries/10
	for key := range r.info {
		delete(r.info, key)
		target--
		if target <= 0 {
			break
		}
	}
}

func (r *Registry) evictDomainsLocked() {
	now := time.Now()
	for ip, entry := range r.ipDomains {
		if now.After(entry.expires) {
			delete(r.ipDomains, ip)
		}
	}
	if len(r.ipDomains) < r.maxEntries {
		return
	}
	target := len(r.ipDomains) - r.maxEntries + r.maxEntries/10
	for ip := range r.ipDomains {
		delete(r.ipDomains, ip)
		target--
		if target <= 0 {
			break
		}
	}
}
