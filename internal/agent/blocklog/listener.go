// Package blocklog turns nftables NFLOG drop records into block events with
// rule attribution resolved from the local policy.
package blocklog

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/florianl/go-nflog/v2"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/agent/docker"
	"github.com/kurwall/kurwall/internal/agent/inspect"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/model"
	"github.com/kurwall/kurwall/internal/agent/policy"
)

// Matcher resolves IP rules.
type Matcher interface {
	MatchIP(addr netip.Addr) (policy.Rule, bool)
}

// Containers resolves container identities.
type Containers interface {
	Lookup(ip netip.Addr) (docker.Container, bool)
}

// Emitter receives block events.
type Emitter interface {
	RecordBlock(model.Block)
}

// Listener reads a kernel NFLOG group.
type Listener struct {
	group      uint16
	matcher    Matcher
	registry   *inspect.Registry
	containers Containers
	emitter    Emitter
	metrics    *metrics.Metrics
	logger     zerolog.Logger

	mu     sync.Mutex
	recent map[string]time.Time
}

// New builds the block log listener.
func New(group uint16, matcher Matcher, registry *inspect.Registry, containers Containers,
	emitter Emitter, m *metrics.Metrics, logger zerolog.Logger) *Listener {
	return &Listener{
		group: group, matcher: matcher, registry: registry, containers: containers,
		emitter: emitter, metrics: m, logger: logger, recent: map[string]time.Time{},
	}
}

// Run reads the NFLOG group until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) error {
	nf, err := nflog.Open(&nflog.Config{
		Group: l.group, Copymode: nflog.CopyPacket, Bufsize: 256, QThresh: 1,
	})
	if err != nil {
		return fmt.Errorf("open nflog group %d: %w", l.group, err)
	}
	l.logger.Info().Uint16("group", l.group).Msg("block log listener started")
	if err := nf.RegisterWithErrorFunc(ctx, l.handle, func(err error) int {
		if ctx.Err() != nil {
			return 1
		}
		l.metrics.BlockLogErrors.Inc()
		l.logger.Debug().Err(err).Msg("nflog error")
		return 0
	}); err != nil {
		_ = nf.Close()
		return err
	}
	<-ctx.Done()
	return nil
}

func (l *Listener) handle(a nflog.Attribute) int {
	if a.Payload == nil || len(*a.Payload) < 20 {
		return 0
	}
	packet, src, dst, srcPort, dstPort, proto, ok := parse(*a.Payload)
	if !ok {
		return 0
	}
	rule, matched := l.matcher.MatchIP(dst)
	if !matched {
		// TLS-domain drops are recorded by the TLS inspector.
		return 0
	}
	now := time.Now()
	container, _ := l.containers.Lookup(src)
	key := rule.ID + "|" + dst.String() + "|" + container.ID
	l.mu.Lock()
	if last, ok := l.recent[key]; ok && now.Sub(last) < 10*time.Second {
		l.mu.Unlock()
		return 0
	}
	if len(l.recent) > 10000 {
		l.recent = map[string]time.Time{}
	}
	l.recent[key] = now
	l.mu.Unlock()

	domain := l.registry.DomainForIP(dst)
	kind := rule.Kind
	target := dst.String()
	reason := "destination matched " + rule.Kind + " rule"
	if rule.Kind == "asn" {
		kind = "asn"
		target = rule.Value
		reason = "destination matched ASN rule"
	}
	l.registry.UpdateInfo(inspect.Key{Src: src, Dst: dst, SrcPort: srcPort, DstPort: dstPort}, inspect.Info{
		Blocked: true, RuleID: rule.ID, Reason: reason, ContainerID: container.ID,
		Container: container.Name, Network: container.Network,
	})
	l.metrics.Blocks.WithLabelValues(kind).Inc()
	l.emitter.RecordBlock(model.Block{
		At: now, Kind: kind, Target: target, SrcIP: src, DstIP: dst,
		SrcPort: srcPort, DstPort: dstPort, Protocol: proto, Domain: domain,
		ContainerID: container.ID, ContainerName: container.Name, RuleID: rule.ID,
		Reason: reason,
	})
	_ = packet
	return 0
}

func parse(raw []byte) (gopacket.Packet, netip.Addr, netip.Addr, uint16, uint16, uint8, bool) {
	var first gopacket.LayerType
	switch raw[0] >> 4 {
	case 4:
		first = layers.LayerTypeIPv4
	case 6:
		first = layers.LayerTypeIPv6
	default:
		return nil, netip.Addr{}, netip.Addr{}, 0, 0, 0, false
	}
	packet := gopacket.NewPacket(raw, first, gopacket.DecodeOptions{Lazy: false, NoCopy: true})
	var srcIP, dstIP net.IP
	var proto uint8
	if v4, ok := packet.NetworkLayer().(*layers.IPv4); ok {
		srcIP, dstIP, proto = v4.SrcIP, v4.DstIP, uint8(v4.Protocol)
	} else if v6, ok := packet.NetworkLayer().(*layers.IPv6); ok {
		srcIP, dstIP, proto = v6.SrcIP, v6.DstIP, uint8(v6.NextHeader)
	} else {
		return nil, netip.Addr{}, netip.Addr{}, 0, 0, 0, false
	}
	src, ok := netip.AddrFromSlice(srcIP)
	if !ok {
		return nil, netip.Addr{}, netip.Addr{}, 0, 0, 0, false
	}
	dst, ok := netip.AddrFromSlice(dstIP)
	if !ok {
		return nil, netip.Addr{}, netip.Addr{}, 0, 0, 0, false
	}
	var srcPort, dstPort uint16
	if tcp, ok := packet.TransportLayer().(*layers.TCP); ok {
		srcPort, dstPort = uint16(tcp.SrcPort), uint16(tcp.DstPort)
	} else if udp, ok := packet.TransportLayer().(*layers.UDP); ok {
		srcPort, dstPort = uint16(udp.SrcPort), uint16(udp.DstPort)
	}
	return packet, src.Unmap(), dst.Unmap(), srcPort, dstPort, proto, true
}
