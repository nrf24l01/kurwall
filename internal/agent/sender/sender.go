// Package sender delivers durable outbox batches to the coordinator in order,
// retrying unacknowledged batches with exponential backoff.
package sender

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/store"
	"github.com/kurwall/kurwall/internal/agent/transport"
)

// Transport is the event delivery API of the gRPC client.
type Transport interface {
	WaitReady(ctx context.Context) error
	SendBatch(ctx context.Context, batch *kurwallv1.EventBatch) (bool, error)
}

// Config controls retry behavior.
type Config struct {
	BatchFetch  int
	RetryMin    time.Duration
	RetryMax    time.Duration
	MaxAttempts int
	SendTimeout time.Duration
}

// Sender drains the outbox.
type Sender struct {
	store   *store.Store
	client  Transport
	cfg     Config
	metrics *metrics.Metrics
	logger  zerolog.Logger
	wake    <-chan struct{}
}

// New builds a sender.
func New(st *store.Store, client Transport, cfg Config, wake <-chan struct{}, m *metrics.Metrics, logger zerolog.Logger) *Sender {
	if cfg.BatchFetch <= 0 {
		cfg.BatchFetch = 32
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = time.Second
	}
	if cfg.RetryMax < cfg.RetryMin {
		cfg.RetryMax = 60 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 30 * time.Second
	}
	return &Sender{store: st, client: client, cfg: cfg, wake: wake, metrics: m, logger: logger}
}

// Run sends batches until ctx is cancelled.
func (s *Sender) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.client.WaitReady(ctx); err != nil {
			return
		}
		if err := s.drain(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.logger.Warn().Err(err).Msg("event sender paused")
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(time.Second):
		}
		s.updateGauges(ctx)
	}
}

func (s *Sender) drain(ctx context.Context) error {
	batches, err := s.store.PendingBatches(ctx, time.Now(), s.cfg.BatchFetch)
	if err != nil {
		return err
	}
	for _, queued := range batches {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch := &kurwallv1.EventBatch{}
		if err := proto.Unmarshal(queued.Payload, batch); err != nil {
			dropped, _ := s.store.DropBatch(ctx, queued.ID)
			s.metrics.EventsDropped.WithLabelValues("corrupt").Add(float64(dropped))
			s.logger.Error().Err(err).Str("batch_id", queued.BatchID).Int("events", dropped).
				Msg("dropping unreadable outbox batch")
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
		duplicate, err := s.client.SendBatch(sendCtx, batch)
		cancel()
		if err == nil {
			if err := s.store.DeleteBatch(ctx, queued.ID); err != nil {
				s.logger.Error().Err(err).Str("batch_id", queued.BatchID).Msg("failed to delete acknowledged batch")
			}
			if duplicate {
				s.logger.Debug().Str("batch_id", queued.BatchID).Msg("coordinator reported duplicate batch")
			}
			continue
		}

		attempts := queued.Attempts + 1
		retryable := transport.IsRetryable(err)
		// Retryable failures (coordinator or storage outage) keep their data until
		// the outbox capacity policy decides otherwise. Only permanently invalid
		// batches are discarded after the attempt limit.
		if !retryable && attempts >= s.cfg.MaxAttempts {
			dropped, _ := s.store.DropBatch(ctx, queued.ID)
			s.metrics.EventsDropped.WithLabelValues("undeliverable").Add(float64(dropped))
			s.logger.Error().Err(err).Str("batch_id", queued.BatchID).Int("attempts", attempts).
				Int("events", dropped).Bool("retryable", retryable).
				Msg("dropping invalid event batch after maximum attempts")
			continue
		}
		delay := backoff(attempts, s.cfg.RetryMin, s.cfg.RetryMax)
		if !retryable {
			delay = s.cfg.RetryMax
		}
		if err := s.store.RescheduleBatch(ctx, queued.ID, attempts, time.Now().Add(delay)); err != nil {
			s.logger.Error().Err(err).Str("batch_id", queued.BatchID).Msg("failed to reschedule batch")
		}
		s.logger.Warn().Err(err).Str("batch_id", queued.BatchID).Int("attempts", attempts).
			Dur("retry_in", delay).Bool("retryable", retryable).Msg("event batch delivery failed")
		if !retryable {
			return err
		}
	}
	return nil
}

func (s *Sender) updateGauges(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	batches, events, err := s.store.Counts(ctx)
	if err != nil {
		return
	}
	s.metrics.OutboxBatches.Set(float64(batches))
	s.metrics.OutboxEvents.Set(float64(events))
}

func backoff(attempt int, min, max time.Duration) time.Duration {
	d := min
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	delta := d / 5
	if delta <= 0 {
		return d
	}
	return d - delta + time.Duration(rand.Int63n(int64(2*delta)+1))
}
