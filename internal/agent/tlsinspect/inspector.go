package tlsinspect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/florianl/go-nfqueue/v2"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/agent/docker"
	"github.com/kurwall/kurwall/internal/agent/firewall"
	"github.com/kurwall/kurwall/internal/agent/inspect"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/model"
	"github.com/kurwall/kurwall/internal/agent/policy"
)

// Matcher resolves domain rules.
type Matcher interface {
	MatchDomain(host string) (policy.Rule, bool)
}

// Containers resolves container identities.
type Containers interface {
	Lookup(ip netip.Addr) (docker.Container, bool)
}

// Emitter receives blocking events.
type Emitter interface {
	RecordBlock(model.Block)
}

type tcpPacket struct {
	version  uint8
	src, dst net.IP
	srcAddr  netip.Addr
	dstAddr  netip.Addr
	srcPort  uint16
	dstPort  uint16
	payload  []byte
}

// Inspector extracts SNI from the first TLS handshake packet and drops
// connections whose domain matches a block rule.
var errMalformed = errors.New("malformed tls packet")

type tlsBuffer struct {
	data    []byte
	first   time.Time
	packets int
}

type Inspector struct {
	queue      uint16
	maxLen     uint32
	maxPacket  uint32
	blockedTTL time.Duration
	matcher    Matcher
	registry   *inspect.Registry
	containers Containers
	emitter    Emitter
	metrics    *metrics.Metrics
	logger     zerolog.Logger
	nf         *nfqueue.Nfqueue

	mu      sync.Mutex
	buffers map[inspect.Key]*tlsBuffer
}

// New builds the TLS inspector.
func New(queue uint16, maxLen, maxPacket uint32, blockedTTL time.Duration, matcher Matcher,
	registry *inspect.Registry, containers Containers, emitter Emitter, m *metrics.Metrics, logger zerolog.Logger) *Inspector {
	return &Inspector{
		queue: queue, maxLen: maxLen, maxPacket: maxPacket, blockedTTL: blockedTTL,
		matcher: matcher, registry: registry, containers: containers, emitter: emitter,
		metrics: m, logger: logger, buffers: map[inspect.Key]*tlsBuffer{},
	}
}

// Run processes the TLS NFQUEUE until ctx is cancelled.
func (i *Inspector) Run(ctx context.Context) error {
	nf, err := nfqueue.Open(&nfqueue.Config{
		NfQueue:      i.queue,
		MaxQueueLen:  i.maxLen,
		MaxPacketLen: i.maxPacket,
		Copymode:     nfqueue.NfQnlCopyPacket,
		Flags:        nfqueue.NfQaCfgFlagFailOpen | nfqueue.NfQaCfgFlagConntrack,
	})
	if err != nil {
		return fmt.Errorf("open tls nfqueue: %w", err)
	}
	i.nf = nf
	i.logger.Info().Uint16("queue", i.queue).Msg("tls inspector started")
	go i.cleanupLoop(ctx)
	if err := nf.RegisterWithErrorFunc(ctx, i.handle, func(err error) int {
		if ctx.Err() != nil {
			return 1
		}
		i.metrics.QueueErrors.WithLabelValues("tls").Inc()
		i.logger.Warn().Err(err).Msg("tls nfqueue error")
		return 0
	}); err != nil {
		_ = nf.Close()
		return err
	}
	<-ctx.Done()
	return nil
}

func (i *Inspector) handle(a nfqueue.Attribute) int {
	if a.PacketID == nil || a.Payload == nil {
		return 0
	}
	id := *a.PacketID
	accept := func(mark bool) {
		verdict := nfqueue.NfAccept
		if mark {
			_ = i.nf.SetVerdictWithConnMark(id, verdict, int(firewall.InspectedMark))
			return
		}
		_ = i.nf.SetVerdict(id, verdict)
	}

	i.metrics.QueuePackets.WithLabelValues("tls").Inc()
	packet, err := parseTCP(*a.Payload)
	if err != nil {
		accept(true)
		return 0
	}
	key := inspect.Key{Src: packet.srcAddr, Dst: packet.dstAddr, SrcPort: packet.srcPort, DstPort: packet.dstPort}

	if i.registry.IsBlocked(key) {
		_ = i.nf.SetVerdictWithConnMark(id, nfqueue.NfDrop, int(firewall.BlockedMark))
		i.metrics.InspectorDrops.WithLabelValues("tls").Inc()
		return 0
	}
	if len(packet.payload) == 0 {
		// Handshake ACKs carry no payload. Do not mark the connection inspected
		// yet: the ClientHello follows.
		accept(false)
		return 0
	}

	sni, status, expired := i.feed(key, packet.payload)
	switch status {
	case Incomplete:
		if expired {
			accept(true)
			return 0
		}
		accept(false)
		return 0
	case NotTLS, NoSNI:
		accept(true)
		return 0
	}

	container, _ := i.containers.Lookup(key.Src)
	rule, blocked := i.matcher.MatchDomain(sni)
	info := inspect.Info{SNI: sni, SeenAt: time.Now()}
	if container.ID != "" {
		info.ContainerID = container.ID
		info.Container = container.Name
		info.Network = container.Network
	}
	if !blocked {
		i.registry.UpdateInfo(key, info)
		i.registry.RememberDomain(key.Dst, sni, 10*time.Minute)
		accept(true)
		return 0
	}

	info.Blocked = true
	info.RuleID = rule.ID
	info.Reason = "SNI matched domain rule"
	i.registry.UpdateInfo(key, info)
	i.registry.MarkBlocked(key, i.blockedTTL)
	i.registry.RememberDomain(key.Dst, sni, 10*time.Minute)
	i.metrics.Blocks.WithLabelValues("domain").Inc()
	i.metrics.InspectorDrops.WithLabelValues("tls").Inc()
	i.emitter.RecordBlock(model.Block{
		At: time.Now(), Kind: "domain", Target: sni, SrcIP: key.Src, DstIP: key.Dst,
		SrcPort: key.SrcPort, DstPort: key.DstPort, Protocol: 6, Domain: sni,
		ContainerID: container.ID, ContainerName: container.Name, RuleID: rule.ID,
		Reason: "SNI matched domain rule",
	})
	i.logger.Debug().Str("sni", sni).Str("rule_id", rule.ID).Msg("blocked tls connection")
	_ = i.nf.SetVerdictWithConnMark(id, nfqueue.NfDrop, int(firewall.BlockedMark))
	return 0
}

// feed reassembles up to a few TCP segments of a ClientHello. It returns the
// parse status and whether the buffer should be abandoned.
func (i *Inspector) feed(key inspect.Key, payload []byte) (string, Status, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	buf, ok := i.buffers[key]
	if !ok {
		buf = &tlsBuffer{first: time.Now()}
		i.buffers[key] = buf
		if len(i.buffers) > 20000 {
			i.buffers = map[inspect.Key]*tlsBuffer{key: buf}
		}
	}
	buf.data = append(buf.data, payload...)
	buf.packets++
	if len(buf.data) > 16384 {
		buf.data = buf.data[:16384]
	}
	sni, status := InspectClientHello(buf.data)
	expired := time.Since(buf.first) > 10*time.Second || buf.packets > 12
	if status != Incomplete || expired {
		delete(i.buffers, key)
	}
	return sni, status, expired
}

func (i *Inspector) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-30 * time.Second)
			i.mu.Lock()
			for key, buf := range i.buffers {
				if buf.first.Before(cutoff) {
					delete(i.buffers, key)
				}
			}
			i.mu.Unlock()
		}
	}
}

func parseTCP(raw []byte) (*tcpPacket, error) {
	if len(raw) < 1 {
		return nil, errMalformed
	}
	var first gopacket.LayerType
	switch raw[0] >> 4 {
	case 4:
		first = layers.LayerTypeIPv4
	case 6:
		first = layers.LayerTypeIPv6
	default:
		return nil, errMalformed
	}
	packet := gopacket.NewPacket(raw, first, gopacket.DecodeOptions{Lazy: false, NoCopy: true})
	tcp, ok := packet.TransportLayer().(*layers.TCP)
	if !ok {
		return nil, errMalformed
	}
	out := &tcpPacket{srcPort: uint16(tcp.SrcPort), dstPort: uint16(tcp.DstPort), payload: tcp.Payload}
	if v4, ok := packet.NetworkLayer().(*layers.IPv4); ok {
		out.version = 4
		out.src, out.dst = v4.SrcIP, v4.DstIP
	} else if v6, ok := packet.NetworkLayer().(*layers.IPv6); ok {
		out.version = 6
		out.src, out.dst = v6.SrcIP, v6.DstIP
	} else {
		return nil, errMalformed
	}
	src, ok := netip.AddrFromSlice(out.src)
	if !ok {
		return nil, errMalformed
	}
	dst, ok := netip.AddrFromSlice(out.dst)
	if !ok {
		return nil, errMalformed
	}
	out.srcAddr, out.dstAddr = src.Unmap(), dst.Unmap()
	return out, nil
}
