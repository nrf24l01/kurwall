package dns

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/florianl/go-nfqueue/v2"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/agent/docker"
	"github.com/kurwall/kurwall/internal/agent/inspect"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/model"
	"github.com/kurwall/kurwall/internal/agent/policy"
	"github.com/kurwall/kurwall/internal/platform/id"
)

// Matcher resolves domain rules.
type Matcher interface {
	MatchDomain(host string) (policy.Rule, bool)
}

// Containers resolves container identities.
type Containers interface {
	Lookup(ip netip.Addr) (docker.Container, bool)
}

// Emitter receives observed events.
type Emitter interface {
	RecordDNS(model.DNS)
	RecordBlock(model.Block)
}

type pendingKey struct {
	client netip.Addr
	port   uint16
	id     uint16
}

type pendingQuery struct {
	msg       *dnsMessage
	container docker.Container
	hasBox    bool
	at        time.Time
}

// Inspector parses DNS queries and responses from NFQUEUE and rewrites blocked
// UDP queries into NXDOMAIN answers.
type Inspector struct {
	queue      uint16
	maxLen     uint32
	maxPacket  uint32
	timeout    time.Duration
	matcher    Matcher
	registry   *inspect.Registry
	containers Containers
	emitter    Emitter
	metrics    *metrics.Metrics
	logger     zerolog.Logger

	mu      sync.Mutex
	pending map[pendingKey]pendingQuery
	nf      *nfqueue.Nfqueue
}

// New builds the DNS inspector.
func New(queue uint16, maxLen, maxPacket uint32, timeout time.Duration, matcher Matcher,
	registry *inspect.Registry, containers Containers, emitter Emitter, m *metrics.Metrics, logger zerolog.Logger) *Inspector {
	return &Inspector{
		queue: queue, maxLen: maxLen, maxPacket: maxPacket, timeout: timeout,
		matcher: matcher, registry: registry, containers: containers, emitter: emitter,
		metrics: m, logger: logger, pending: map[pendingKey]pendingQuery{},
	}
}

// Run opens the queue and processes packets until ctx is cancelled.
func (i *Inspector) Run(ctx context.Context) error {
	nf, err := nfqueue.Open(&nfqueue.Config{
		NfQueue:      i.queue,
		MaxQueueLen:  i.maxLen,
		MaxPacketLen: i.maxPacket,
		Copymode:     nfqueue.NfQnlCopyPacket,
		Flags:        nfqueue.NfQaCfgFlagFailOpen,
	})
	if err != nil {
		return fmt.Errorf("open dns nfqueue: %w", err)
	}
	i.mu.Lock()
	i.nf = nf
	i.mu.Unlock()

	go i.flushLoop(ctx)
	i.logger.Info().Uint16("queue", i.queue).Msg("dns inspector started")
	if err := nf.RegisterWithErrorFunc(ctx, i.handle, func(err error) int {
		if ctx.Err() != nil {
			return 1
		}
		i.metrics.QueueErrors.WithLabelValues("dns").Inc()
		i.logger.Warn().Err(err).Msg("dns nfqueue error")
		return 0
	}); err != nil {
		_ = nf.Close()
		return err
	}
	// RegisterWithErrorFunc starts the receive loop asynchronously. Keep the
	// socket open for the lifetime of the agent; closing it concurrently is
	// unsafe in the upstream library.
	<-ctx.Done()
	return nil
}

func (i *Inspector) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			i.flushExpired()
		}
	}
}

func (i *Inspector) flushExpired() {
	cutoff := time.Now().Add(-i.timeout)
	var expired []pendingQuery
	i.mu.Lock()
	for key, query := range i.pending {
		if query.at.Before(cutoff) {
			expired = append(expired, query)
			delete(i.pending, key)
		}
	}
	i.mu.Unlock()
	for _, query := range expired {
		i.emitQuery(model.DNS{
			At: query.at, ClientIP: query.msg.clientIP, ClientPort: fmt.Sprintf("%d", query.msg.clientPort),
			QName: query.msg.qname, QType: query.msg.qtype, ContainerID: query.container.ID,
			ContainerName: query.container.Name, LatencyMS: 0, RCode: 0,
		})
	}
}

func (i *Inspector) handle(a nfqueue.Attribute) int {
	if a.PacketID == nil || a.Payload == nil {
		return 0
	}
	id := *a.PacketID
	reply := func(verdict int, altered []byte) {
		var err error
		if altered != nil {
			err = i.nf.SetVerdictModPacket(id, verdict, altered)
		} else {
			err = i.nf.SetVerdict(id, verdict)
		}
		if err != nil {
			i.metrics.QueueErrors.WithLabelValues("dns").Inc()
			i.logger.Debug().Err(err).Msg("failed to set dns verdict")
		}
	}

	i.metrics.QueuePackets.WithLabelValues("dns").Inc()
	dp, msg, err := parsePacket(*a.Payload)
	if err != nil {
		reply(nfqueue.NfAccept, nil)
		return 0
	}
	msg.clientIP, msg.clientPort = clientSide(dp, msg.query)
	if msg.query {
		i.handleQuery(dp, msg, reply)
		return 0
	}
	i.handleResponse(dp, msg)
	reply(nfqueue.NfAccept, nil)
	return 0
}

func clientSide(dp *dnsPacket, query bool) (netip.Addr, uint16) {
	if query {
		return dp.srcAddr, dp.srcPort
	}
	return dp.dstAddr, dp.dstPort
}

func (i *Inspector) handleQuery(dp *dnsPacket, msg *dnsMessage, reply func(int, []byte)) {
	container, hasBox := i.containers.Lookup(msg.clientIP)
	rule, blocked := i.matcher.MatchDomain(msg.qname)
	i.metrics.DNSQueries.Inc()
	if !blocked {
		key := pendingKey{client: msg.clientIP, port: msg.clientPort, id: msg.id}
		i.mu.Lock()
		if len(i.pending) >= 10000 {
			i.pending = map[pendingKey]pendingQuery{}
		}
		i.pending[key] = pendingQuery{msg: msg, container: container, hasBox: hasBox, at: time.Now()}
		i.mu.Unlock()
		reply(nfqueue.NfAccept, nil)
		return
	}

	// A rewritten NXDOMAIN only makes sense for UDP. TCP DNS is allowed but
	// still recorded and blocked later at the TLS layer.
	var altered []byte
	if dp.proto == 17 {
		if response, err := buildNXDomain(dp); err == nil {
			altered = response
		} else {
			i.logger.Debug().Err(err).Msg("failed to build nxdomain response")
		}
	}
	reply(nfqueue.NfAccept, altered)
	i.metrics.Blocks.WithLabelValues("dns").Inc()
	rcode := uint8(0)
	if altered != nil {
		rcode = 3
	}
	i.emitQuery(model.DNS{
		At: time.Now(), ClientIP: msg.clientIP, ClientPort: fmt.Sprintf("%d", msg.clientPort),
		QName: msg.qname, QType: msg.qtype, RCode: rcode, Blocked: true, RuleID: rule.ID,
		ContainerID: container.ID, ContainerName: container.Name,
	})
	i.emitter.RecordBlock(model.Block{
		At: time.Now(), Kind: "dns", Target: msg.qname, SrcIP: msg.clientIP, DstIP: dp.dstAddr,
		SrcPort: msg.clientPort, DstPort: dp.dstPort, Protocol: dp.proto, Domain: msg.qname,
		ContainerID: container.ID, ContainerName: container.Name, RuleID: rule.ID,
		Reason: "domain rule matched DNS query",
	})
}

func (i *Inspector) handleResponse(dp *dnsPacket, msg *dnsMessage) {
	key := pendingKey{client: msg.clientIP, port: msg.clientPort, id: msg.id}
	i.mu.Lock()
	query, ok := i.pending[key]
	if ok {
		delete(i.pending, key)
	}
	i.mu.Unlock()

	for idx, addr := range msg.answers {
		ttl := time.Duration(msg.ttls[idx]) * time.Second
		i.registry.RememberDomain(addr, msg.qname, ttl)
	}
	if !ok {
		return
	}
	latency := time.Since(query.at).Milliseconds()
	latencyMS := uint32(0)
	if latency > 0 && latency < 1<<32 {
		latencyMS = uint32(latency)
	}
	i.emitQuery(model.DNS{
		At: query.at, ClientIP: msg.clientIP, ClientPort: fmt.Sprintf("%d", msg.clientPort),
		QName: query.msg.qname, QType: query.msg.qtype, RCode: msg.rcode, Answers: msg.answers,
		ContainerID: query.container.ID, ContainerName: query.container.Name, LatencyMS: latencyMS,
	})
}

func (i *Inspector) emitQuery(event model.DNS) {
	event.ID = id.NewV7()
	if event.At.IsZero() {
		event.At = time.Now()
	}
	i.emitter.RecordDNS(event)
}
