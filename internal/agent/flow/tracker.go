// Package flow tracks completed connections through Linux conntrack. One
// consolidated event is emitted per completed flow with final byte counters.
package flow

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/rs/zerolog"
	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"

	"github.com/kurwall/kurwall/internal/agent/docker"
	"github.com/kurwall/kurwall/internal/agent/inspect"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/model"
)

// Containers resolves container identities by IP.
type Containers interface {
	Lookup(ip netip.Addr) (docker.Container, bool)
}

// Local reports whether an IP belongs to this host.
type Local interface {
	Contains(addr netip.Addr) bool
}

// Emitter receives completed flow records.
type Emitter interface {
	RecordFlow(model.Flow)
}

type state struct {
	key        inspect.Key
	start      time.Time
	bytesIn    uint64
	bytesOut   uint64
	packetsIn  uint64
	packetsOut uint64
	lastSeen   time.Time
	done       bool
}

// Tracker listens to conntrack events.
type Tracker struct {
	registry   *inspect.Registry
	containers Containers
	local      Local
	emitter    Emitter
	metrics    *metrics.Metrics
	logger     zerolog.Logger
	dumpEvery  time.Duration

	mu     sync.Mutex
	active map[uint32]state
}

// New builds a flow tracker.
func New(registry *inspect.Registry, containers Containers, local Local, emitter Emitter, m *metrics.Metrics, logger zerolog.Logger, dumpEvery time.Duration) *Tracker {
	if dumpEvery < 200*time.Millisecond {
		dumpEvery = time.Second
	}
	return &Tracker{
		registry: registry, containers: containers, local: local,
		emitter: emitter, metrics: m, logger: logger, dumpEvery: dumpEvery,
		active: map[uint32]state{},
	}
}

// Run consumes conntrack events until ctx is cancelled.
func (t *Tracker) Run(ctx context.Context) error {
	conn, err := conntrack.Dial(&netlink.Config{})
	if err != nil {
		return fmt.Errorf("dial conntrack: %w", err)
	}
	defer conn.Close()

	events := make(chan conntrack.Event, 512)
	errCh, err := conn.Listen(events, 1, netfilter.GroupsCT)
	if err != nil {
		return fmt.Errorf("listen conntrack: %w", err)
	}
	dumpConn, err := conntrack.Dial(&netlink.Config{})
	if err != nil {
		return fmt.Errorf("dial conntrack accounting: %w", err)
	}
	defer dumpConn.Close()
	go t.dumpLoop(ctx, dumpConn)
	t.logger.Info().Dur("snapshot_interval", t.dumpEvery).Msg("flow tracker started")
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("conntrack listener: %w", err)
			}
			return nil
		case event := <-events:
			t.handle(event)
		}
	}
}

func (t *Tracker) handle(event conntrack.Event) {
	if event.Flow == nil {
		return
	}
	switch event.Type {
	case conntrack.EventNew:
		t.handleNew(event.Flow)
	case conntrack.EventDestroy:
		t.handleDestroy(event.Flow)
	}
}

func (t *Tracker) handleNew(f *conntrack.Flow) {
	proto := f.TupleOrig.Proto.Protocol
	if proto != 6 && proto != 17 {
		return
	}
	key, ok := keyFromFlow(f)
	if !ok {
		return
	}
	start := f.Timestamp.Start
	if start.IsZero() {
		start = time.Now()
	}
	t.mu.Lock()
	existing, ok := t.active[f.ID]
	if !ok {
		existing = state{key: key, start: start}
	}
	existing.key = key
	if !f.Timestamp.Start.IsZero() {
		existing.start = f.Timestamp.Start
	}
	existing.lastSeen = time.Now()
	t.active[f.ID] = existing
	if len(t.active) > 200000 {
		// Guard against missed DESTROY events.
		t.active = map[uint32]state{f.ID: existing}
	}
	t.mu.Unlock()
}

func (t *Tracker) handleDestroy(f *conntrack.Flow) {
	proto := f.TupleOrig.Proto.Protocol
	if proto != 6 && proto != 17 {
		return
	}
	key, ok := keyFromFlow(f)
	if !ok {
		return
	}
	if key.SrcPort == 53 || key.DstPort == 53 || (key.Src.IsLoopback() && key.Dst.IsLoopback()) {
		t.registry.Forget(key)
		return
	}

	t.mu.Lock()
	st, seen := t.active[f.ID]
	delete(t.active, f.ID)
	t.mu.Unlock()
	if seen && st.done {
		t.registry.Forget(key)
		return
	}
	t.complete(f, key, st, seen, time.Now().UTC())
}

type finalFlow struct {
	flow conntrack.Flow
	key  inspect.Key
	st   state
}

// complete enriches and emits one completed connection.
func (t *Tracker) complete(f *conntrack.Flow, key inspect.Key, st state, seen bool, end time.Time) {
	start := f.Timestamp.Start
	if start.IsZero() && seen {
		start = st.start
	}
	if start.IsZero() {
		start = end
	}
	if !f.Timestamp.Stop.IsZero() && !f.Timestamp.Stop.Before(start) {
		end = f.Timestamp.Stop
	}
	if end.Before(start) {
		end = time.Now().UTC()
	}
	bytesOut, bytesIn := f.CountersOrig.Bytes, f.CountersReply.Bytes
	packetsOut, packetsIn := f.CountersOrig.Packets, f.CountersReply.Packets
	if seen {
		if st.bytesOut > bytesOut {
			bytesOut = st.bytesOut
		}
		if st.bytesIn > bytesIn {
			bytesIn = st.bytesIn
		}
		if st.packetsOut > packetsOut {
			packetsOut = st.packetsOut
		}
		if st.packetsIn > packetsIn {
			packetsIn = st.packetsIn
		}
	}

	container, isContainer := t.containers.Lookup(key.Src)
	outbound := isContainer || t.local.Contains(key.Src)
	if !outbound {
		t.registry.Forget(key)
		return
	}

	info, _ := t.registry.Info(key)
	domain := info.Domain
	if domain == "" {
		domain = t.registry.DomainForIP(key.Dst)
	}
	if domain == "" {
		domain = info.SNI
	}
	containerID := info.ContainerID
	containerName := info.Container
	network := info.Network
	if containerName == "" && isContainer {
		containerID, containerName, network = container.ID, container.Name, container.Network
	}
	reason := info.Reason
	if reason == "" {
		if info.Blocked {
			reason = "blocked"
		} else {
			reason = "allowed"
		}
	}
	t.registry.Forget(key)

	flow := model.Flow{
		StartedAt: start.UTC(), EndedAt: end.UTC(),
		SrcIP: key.Src, DstIP: key.Dst, SrcPort: key.SrcPort, DstPort: key.DstPort,
		Protocol: f.TupleOrig.Proto.Protocol, Domain: domain, SNI: info.SNI,
		ContainerID: containerID, ContainerName: containerName, Network: network,
		BytesOut: bytesOut, BytesIn: bytesIn,
		PacketsOut: packetsOut, PacketsIn: packetsIn,
		Blocked: info.Blocked, RuleID: info.RuleID, Reason: reason,
	}
	t.metrics.Flows.Inc()
	t.metrics.Bytes.WithLabelValues("out").Add(float64(flow.BytesOut))
	t.metrics.Bytes.WithLabelValues("in").Add(float64(flow.BytesIn))
	t.metrics.Packets.WithLabelValues("out").Add(float64(flow.PacketsOut))
	t.metrics.Packets.WithLabelValues("in").Add(float64(flow.PacketsIn))
	t.emitter.RecordFlow(flow)
}

// dumpLoop snapshots conntrack counters. Kernel event notifications do not
// carry accounting counters, so a periodic dump supplies byte and packet
// totals for completed flows.
func (t *Tracker) dumpLoop(ctx context.Context, conn *conntrack.Conn) {
	t.snapshot(conn)
	ticker := time.NewTicker(t.dumpEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.snapshot(conn)
		}
	}
}

func (t *Tracker) snapshot(conn *conntrack.Conn) {
	flows, err := conn.Dump(nil)
	if err != nil {
		t.metrics.FlowDumpErrors.Inc()
		t.logger.Warn().Err(err).Msg("conntrack accounting snapshot failed")
		return
	}
	now := time.Now()
	seen := make(map[uint32]struct{}, len(flows))
	var finals []finalFlow
	t.mu.Lock()
	for _, f := range flows {
		proto := f.TupleOrig.Proto.Protocol
		if proto != 6 && proto != 17 {
			continue
		}
		key, ok := keyFromFlow(&f)
		if !ok || key.SrcPort == 53 || key.DstPort == 53 || (key.Src.IsLoopback() && key.Dst.IsLoopback()) {
			continue
		}
		seen[f.ID] = struct{}{}
		st, ok := t.active[f.ID]
		if !ok {
			st = state{key: key, start: now}
		}
		st.key = key
		if !f.Timestamp.Start.IsZero() {
			st.start = f.Timestamp.Start
		}
		if f.CountersOrig.Bytes > st.bytesOut {
			st.bytesOut = f.CountersOrig.Bytes
		}
		if f.CountersReply.Bytes > st.bytesIn {
			st.bytesIn = f.CountersReply.Bytes
		}
		if f.CountersOrig.Packets > st.packetsOut {
			st.packetsOut = f.CountersOrig.Packets
		}
		if f.CountersReply.Packets > st.packetsIn {
			st.packetsIn = f.CountersReply.Packets
		}
		st.lastSeen = now
		// Emit TCP connections as soon as conntrack reports a closing state
		// instead of waiting for the TIME_WAIT timeout to expire. The DESTROY
		// event for an already emitted flow is ignored.
		if !st.done && proto == 6 && f.ProtoInfo.TCP != nil && f.ProtoInfo.TCP.State >= 6 && f.ProtoInfo.TCP.State <= 8 {
			st.done = true
			finals = append(finals, finalFlow{flow: f, key: key, st: st})
		}
		t.active[f.ID] = st
	}
	for id, st := range t.active {
		if _, ok := seen[id]; ok {
			continue
		}
		if now.Sub(st.lastSeen) > 10*time.Minute {
			delete(t.active, id)
		}
	}
	t.mu.Unlock()

	for _, final := range finals {
		flow := final.flow
		t.complete(&flow, final.key, final.st, true, now.UTC())
	}
}

func keyFromFlow(f *conntrack.Flow) (inspect.Key, bool) {
	orig := f.TupleOrig
	src := orig.IP.SourceAddress
	dst := orig.IP.DestinationAddress
	if !src.IsValid() || !dst.IsValid() {
		return inspect.Key{}, false
	}
	return inspect.Key{
		Src: src.Unmap(), Dst: dst.Unmap(),
		SrcPort: orig.Proto.SourcePort, DstPort: orig.Proto.DestinationPort,
	}, true
}
