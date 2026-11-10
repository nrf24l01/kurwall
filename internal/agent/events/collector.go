// Package events batches observed events and persists them into the durable
// SQLite outbox before transmission.
package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/model"
	"github.com/kurwall/kurwall/internal/agent/store"
	"github.com/kurwall/kurwall/internal/platform/id"
)

// Config controls batching.
type Config struct {
	BatchMaxEvents   int
	FlushInterval    time.Duration
	MaxOutboxBatches int
	MaxBufferEvents  int
}

// Collector accumulates events and writes batches to the outbox.
type Collector struct {
	store   *store.Store
	cfg     Config
	metrics *metrics.Metrics
	logger  zerolog.Logger
	wake    chan struct{}

	mu      sync.Mutex
	buffer  []*kurwallv1.NetworkEvent
	kinds   []string
	dropped atomic.Uint64
}

// New builds an event collector.
func New(st *store.Store, cfg Config, m *metrics.Metrics, logger zerolog.Logger) *Collector {
	if cfg.MaxBufferEvents <= 0 {
		cfg.MaxBufferEvents = 20000
	}
	return &Collector{store: st, cfg: cfg, metrics: m, logger: logger, wake: make(chan struct{}, 1)}
}

// RecordFlow appends a completed connection.
func (c *Collector) RecordFlow(flow model.Flow) {
	ev := &kurwallv1.NetworkEvent{
		EventId: id.NewV7(),
		Event: &kurwallv1.NetworkEvent_Flow{Flow: &kurwallv1.FlowEvent{
			StartedAtUnixNano: flow.StartedAt.UnixNano(), EndedAtUnixNano: flow.EndedAt.UnixNano(),
			SrcIp: flow.SrcIP.String(), DstIp: flow.DstIP.String(),
			SrcPort: uint32(flow.SrcPort), DstPort: uint32(flow.DstPort), Protocol: uint32(flow.Protocol),
			Domain: flow.Domain, Sni: flow.SNI, ContainerId: flow.ContainerID, ContainerName: flow.ContainerName,
			BytesIn: flow.BytesIn, BytesOut: flow.BytesOut, PacketsIn: flow.PacketsIn, PacketsOut: flow.PacketsOut,
			Blocked: flow.Blocked, RuleId: flow.RuleID, Reason: flow.Reason, Network: flow.Network,
		}},
	}
	c.record(ev, "flow")
}

// RecordDNS appends a DNS request.
func (c *Collector) RecordDNS(event model.DNS) {
	answers := make([]string, 0, len(event.Answers))
	for _, answer := range event.Answers {
		answers = append(answers, answer.String())
	}
	ev := &kurwallv1.NetworkEvent{
		EventId: event.ID,
		Event: &kurwallv1.NetworkEvent_Dns{Dns: &kurwallv1.DnsEvent{
			AtUnixNano: event.At.UnixNano(), ClientIp: event.ClientIP.String(), ClientPort: event.ClientPort,
			Qname: event.QName, Qtype: uint32(event.QType), Rcode: uint32(event.RCode), Answers: answers,
			Blocked: event.Blocked, RuleId: event.RuleID, ContainerId: event.ContainerID,
			ContainerName: event.ContainerName, LatencyMs: event.LatencyMS,
		}},
	}
	c.record(ev, "dns")
}

// RecordBlock appends a blocking decision.
func (c *Collector) RecordBlock(event model.Block) {
	ev := &kurwallv1.NetworkEvent{
		EventId: id.NewV7(),
		Event: &kurwallv1.NetworkEvent_Block{Block: &kurwallv1.BlockEvent{
			AtUnixNano: event.At.UnixNano(), Kind: event.Kind, Target: event.Target,
			SrcIp: event.SrcIP.String(), DstIp: event.DstIP.String(),
			SrcPort: uint32(event.SrcPort), DstPort: uint32(event.DstPort), Protocol: uint32(event.Protocol),
			Domain: event.Domain, ContainerId: event.ContainerID, ContainerName: event.ContainerName,
			RuleId: event.RuleID, Reason: event.Reason,
		}},
	}
	c.record(ev, "block")
}

func (c *Collector) record(ev *kurwallv1.NetworkEvent, kind string) {
	c.mu.Lock()
	c.buffer = append(c.buffer, ev)
	c.kinds = append(c.kinds, kind)
	dropped := 0
	for len(c.buffer) > c.cfg.MaxBufferEvents {
		c.buffer = c.buffer[1:]
		c.kinds = c.kinds[1:]
		dropped++
	}
	c.mu.Unlock()
	if dropped > 0 {
		c.dropped.Add(uint64(dropped))
		c.metrics.EventsDropped.WithLabelValues(kind).Add(float64(dropped))
		c.logger.Error().Int("dropped", dropped).Msg("in-memory event buffer overflow")
	}
}

// Run flushes the buffer on an interval until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.Flush(flushCtx)
			cancel()
			return
		case <-ticker.C:
			flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := c.Flush(flushCtx); err != nil {
				c.logger.Error().Err(err).Msg("failed to flush event buffer")
			}
			cancel()
		}
	}
}

// Flush persists all buffered events as one or more outbox batches.
func (c *Collector) Flush(ctx context.Context) error {
	c.mu.Lock()
	if len(c.buffer) == 0 {
		c.mu.Unlock()
		return nil
	}
	events := c.buffer
	kinds := c.kinds
	c.buffer = nil
	c.kinds = nil
	c.mu.Unlock()

	for start := 0; start < len(events); start += c.cfg.BatchMaxEvents {
		end := start + c.cfg.BatchMaxEvents
		if end > len(events) {
			end = len(events)
		}
		if err := c.persist(ctx, events[start:end], kinds[start:end]); err != nil {
			// Return unpersisted events to the buffer so they are retried.
			c.mu.Lock()
			c.buffer = append(events[start:], c.buffer...)
			c.kinds = append(kinds[start:], c.kinds...)
			c.mu.Unlock()
			return err
		}
	}
	return nil
}

func (c *Collector) persist(ctx context.Context, events []*kurwallv1.NetworkEvent, kinds []string) error {
	if droppedBatches, droppedEvents, err := c.store.Prune(ctx, c.cfg.MaxOutboxBatches-1); err != nil {
		c.logger.Warn().Err(err).Msg("failed to prune event outbox")
	} else if droppedEvents > 0 {
		c.dropped.Add(uint64(droppedEvents))
		c.metrics.EventsDropped.WithLabelValues("outbox").Add(float64(droppedEvents))
		c.logger.Error().Int64("batches", droppedBatches).Int64("events", droppedEvents).
			Msg("outbox capacity exceeded; oldest events were dropped")
	}

	batch := &kurwallv1.EventBatch{
		BatchId:           id.NewV7(),
		Events:            events,
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	payload, err := proto.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encode event batch: %w", err)
	}
	if err := c.store.EnqueueBatch(ctx, batch.BatchId, payload, len(events), time.Now()); err != nil {
		return err
	}
	if batches, queued, err := c.store.Counts(ctx); err == nil {
		c.metrics.OutboxBatches.Set(float64(batches))
		c.metrics.OutboxEvents.Set(float64(queued))
	}
	for i := range events {
		kind := kinds[i]
		c.metrics.EventsRecorded.WithLabelValues(kind).Inc()
	}
	c.signal()
	return nil
}

func (c *Collector) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Wake returns a channel that receives a signal when new batches are stored.
func (c *Collector) Wake() <-chan struct{} { return c.wake }

// DroppedTotal returns the cumulative number of discarded events.
func (c *Collector) DroppedTotal() uint64 { return c.dropped.Load() }

// Buffered returns the number of events not yet persisted.
func (c *Collector) Buffered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buffer)
}
