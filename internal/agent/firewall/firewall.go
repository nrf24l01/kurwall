// Package firewall owns the Kurwall nftables table. It blocks IPv4/IPv6
// addresses and CIDR ranges, and steers DNS/TLS packets to NFQUEUE for
// best-effort domain inspection.
package firewall

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"sync"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Conntrack mark bits reserved by Kurwall.
const (
	InspectedMark uint32 = 0x10000000
	BlockedMark   uint32 = 0x20000000
)

// Rule is a normalized IP or CIDR blocking rule.
type Rule struct {
	ID    string
	Kind  string
	Value string
}

// Key identifies a connection that can be blocked at the packet layer.
type Key struct {
	Src     netip.Addr
	Dst     netip.Addr
	SrcPort uint16
	DstPort uint16
}

// Config controls the nftables integration.
type Config struct {
	TableName string
	DNSQueue  uint16
	TLSQueue  uint16
	BlockLog  uint16
	LogPrefix string
	RateLimit uint64
	RateBurst uint32
}

// Manager owns one nftables table and serializes updates.
type Manager struct {
	conn *nftables.Conn
	cfg  Config
	mu   sync.Mutex
}

// New opens an nftables connection. It requires CAP_NET_ADMIN.
func New(cfg Config) (*Manager, error) {
	if cfg.TableName == "" {
		cfg.TableName = "kurwall"
	}
	if cfg.LogPrefix == "" {
		cfg.LogPrefix = "kurwall:block "
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 20
	}
	if cfg.RateBurst == 0 {
		cfg.RateBurst = 40
	}
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, fmt.Errorf("open nftables: %w", err)
	}
	return &Manager{conn: conn, cfg: cfg}, nil
}

// Apply atomically replaces the Kurwall table with the requested rules.
func (m *Manager) Apply(ctx context.Context, rules []Rule, tlsPorts []uint16, dnsEnabled, tlsEnabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: m.cfg.TableName}
	if err := m.deleteTable(table); err != nil {
		return fmt.Errorf("delete nftables table: %w", err)
	}
	m.conn.AddTable(table)

	if err := m.addSets(table, rules, tlsPorts); err != nil {
		return err
	}
	for _, hook := range []*nftables.ChainHook{nftables.ChainHookOutput, nftables.ChainHookForward} {
		if err := m.addChain(table, *hook, dnsEnabled, tlsEnabled); err != nil {
			return err
		}
	}
	if dnsEnabled {
		if err := m.addInputChain(table); err != nil {
			return err
		}
	}
	if err := m.conn.Flush(); err != nil {
		return fmt.Errorf("apply nftables rules: %w", err)
	}
	return nil
}

func (m *Manager) deleteTable(table *nftables.Table) error {
	tables, err := m.conn.ListTables()
	if err != nil {
		return err
	}
	for _, existing := range tables {
		if existing.Name == table.Name && existing.Family == table.Family {
			m.conn.DelTable(existing)
			break
		}
	}
	return nil
}

func (m *Manager) addSets(table *nftables.Table, rules []Rule, tlsPorts []uint16) error {
	blocked4 := &nftables.Set{
		Table: table, Name: "blocked4", KeyType: nftables.TypeIPAddr,
		Interval: true, AutoMerge: true,
	}
	blocked6 := &nftables.Set{
		Table: table, Name: "blocked6", KeyType: nftables.TypeIP6Addr,
		Interval: true, AutoMerge: true,
	}
	ports := &nftables.Set{
		Table: table, Name: "tls_ports", KeyType: nftables.TypeInetService,
	}
	for _, set := range []*nftables.Set{blocked4, blocked6, ports} {
		if err := m.conn.AddSet(set, nil); err != nil {
			return fmt.Errorf("create set %s: %w", set.Name, err)
		}
	}

	v4, v6 := encodeBlocked(rules)
	if len(v4) > 0 {
		if err := m.conn.SetAddElements(blocked4, v4); err != nil {
			return fmt.Errorf("populate blocked4: %w", err)
		}
	}
	if len(v6) > 0 {
		if err := m.conn.SetAddElements(blocked6, v6); err != nil {
			return fmt.Errorf("populate blocked6: %w", err)
		}
	}
	sortedPorts := append([]uint16(nil), tlsPorts...)
	sort.Slice(sortedPorts, func(i, j int) bool { return sortedPorts[i] < sortedPorts[j] })
	portElements := make([]nftables.SetElement, 0, len(sortedPorts))
	for i, port := range sortedPorts {
		if i > 0 && port == sortedPorts[i-1] {
			continue
		}
		key := make([]byte, 2)
		binary.BigEndian.PutUint16(key, port)
		portElements = append(portElements, nftables.SetElement{Key: key})
	}
	if len(portElements) > 0 {
		if err := m.conn.SetAddElements(ports, portElements); err != nil {
			return fmt.Errorf("populate tls_ports: %w", err)
		}
	}
	return nil
}

func (m *Manager) addChain(table *nftables.Table, hook nftables.ChainHook, dnsEnabled, tlsEnabled bool) error {
	policy := nftables.ChainPolicyAccept
	priority := nftables.ChainPriority(*nftables.ChainPriorityFilter - 5)
	name := "output"
	if hook == *nftables.ChainHookForward {
		name = "forward"
	}
	chain := m.conn.AddChain(&nftables.Chain{
		Name: name, Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: &hook, Priority: &priority, Policy: &policy,
	})

	rules := [][]expr.Any{
		m.logBlockedIPv4(), m.dropBlockedIPv4(),
		m.logBlockedIPv6(), m.dropBlockedIPv6(),
	}
	if tlsEnabled {
		rules = append(rules, m.logBlockedTLS(), m.dropBlockedTLS(), m.queueTLS())
	}
	if dnsEnabled {
		rules = append(rules, m.queueDNS(6, true), m.queueDNS(6, false), m.queueDNS(17, true), m.queueDNS(17, false))
	}
	for _, exprs := range rules {
		m.conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs})
	}
	return nil
}

func matchBlocked4() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: "blocked4"},
	}
}

func matchBlocked6() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
		&expr.Lookup{SourceRegister: 1, SetName: "blocked6"},
	}
}

func (m *Manager) logBlockedIPv4() []expr.Any {
	return append(append(matchBlocked4(), m.rateLimitExpr()), m.logExpr())
}

func (m *Manager) dropBlockedIPv4() []expr.Any {
	return append(matchBlocked4(), &expr.Verdict{Kind: expr.VerdictDrop})
}

func (m *Manager) logBlockedIPv6() []expr.Any {
	return append(append(matchBlocked6(), m.rateLimitExpr()), m.logExpr())
}

func (m *Manager) dropBlockedIPv6() []expr.Any {
	return append(matchBlocked6(), &expr.Verdict{Kind: expr.VerdictDrop})
}

func matchBlockedTLS() []expr.Any {
	blocked := make([]byte, 4)
	binary.NativeEndian.PutUint32(blocked, BlockedMark)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{6}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Lookup{SourceRegister: 1, SetName: "tls_ports"},
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: blocked, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
	}
}

func (m *Manager) logBlockedTLS() []expr.Any {
	return append(append(matchBlockedTLS(), m.rateLimitExpr()), m.logExpr())
}

func (m *Manager) dropBlockedTLS() []expr.Any {
	return append(matchBlockedTLS(), &expr.Verdict{Kind: expr.VerdictDrop})
}

func (m *Manager) queueTLS() []expr.Any {
	inspected := make([]byte, 4)
	binary.NativeEndian.PutUint32(inspected, InspectedMark)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{6}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Lookup{SourceRegister: 1, SetName: "tls_ports"},
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: inspected, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: make([]byte, 4)},
		&expr.Counter{},
		&expr.Queue{Num: m.cfg.TLSQueue, Flag: expr.QueueFlagBypass},
	}
}

// addInputChain inspects DNS responses for host-originated lookups. Outgoing
// queries are seen in OUTPUT, but responses from remote resolvers traverse
// INPUT.
func (m *Manager) addInputChain(table *nftables.Table) error {
	policy := nftables.ChainPolicyAccept
	priority := nftables.ChainPriority(*nftables.ChainPriorityFilter - 5)
	chain := m.conn.AddChain(&nftables.Chain{
		Name: "input", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: &priority, Policy: &policy,
	})
	for _, exprs := range [][]expr.Any{m.queueDNS(6, false), m.queueDNS(17, false)} {
		m.conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs})
	}
	return nil
}

func (m *Manager) queueDNS(proto byte, destination bool) []expr.Any {
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, 53)
	offset := uint32(0) // source port
	if destination {
		offset = 2
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 2, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: offset, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: port},
		&expr.Counter{},
		&expr.Queue{Num: m.cfg.DNSQueue, Flag: expr.QueueFlagBypass},
	}
}

func (m *Manager) logExpr() *expr.Log {
	return &expr.Log{
		Group:   m.cfg.BlockLog,
		Snaplen: 512,
		Data:    []byte(m.cfg.LogPrefix),
		Key:     (1 << unix.NFTA_LOG_GROUP) | (1 << unix.NFTA_LOG_PREFIX) | (1 << unix.NFTA_LOG_SNAPLEN),
	}
}

func (m *Manager) rateLimitExpr() *expr.Limit {
	return &expr.Limit{Type: expr.LimitTypePkts, Rate: m.cfg.RateLimit, Unit: expr.LimitTimeSecond, Burst: m.cfg.RateBurst}
}

// Close releases the netlink connection. The nftables table is intentionally
// left in place so filtering survives an agent restart.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conn.CloseLasting()
}

func (m *Manager) LogPrefix() string { return m.cfg.LogPrefix }
