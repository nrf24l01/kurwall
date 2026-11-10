// Package app wires and runs the Kurwall node agent.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/kurwall/kurwall/internal/agent/blocklog"
	"github.com/kurwall/kurwall/internal/agent/config"
	"github.com/kurwall/kurwall/internal/agent/dns"
	"github.com/kurwall/kurwall/internal/agent/docker"
	"github.com/kurwall/kurwall/internal/agent/events"
	"github.com/kurwall/kurwall/internal/agent/firewall"
	"github.com/kurwall/kurwall/internal/agent/flow"
	"github.com/kurwall/kurwall/internal/agent/inspect"
	"github.com/kurwall/kurwall/internal/agent/metrics"
	"github.com/kurwall/kurwall/internal/agent/netinfo"
	"github.com/kurwall/kurwall/internal/agent/policy"
	"github.com/kurwall/kurwall/internal/agent/sender"
	"github.com/kurwall/kurwall/internal/agent/store"
	"github.com/kurwall/kurwall/internal/agent/tlsinspect"
	"github.com/kurwall/kurwall/internal/agent/transport"
	"github.com/kurwall/kurwall/internal/platform/tlsconfig"
	"github.com/kurwall/kurwall/internal/platform/version"
)

// App owns all agent modules.
type App struct {
	cfg        config.Config
	logger     zerolog.Logger
	store      *store.Store
	metrics    *metrics.Metrics
	registry   *inspect.Registry
	containers *docker.Tracker
	local      *netinfo.Local
	firewall   *firewall.Manager
	policy     *policy.Manager
	collector  *events.Collector
	client     *transport.Client
	sender     *sender.Sender
	inspectors bool
}

// New builds the agent. Privileged subsystems (nftables, conntrack, NFQUEUE)
// are opened here so configuration or permission errors fail fast.
func New(ctx context.Context, cfg config.Config, logger zerolog.Logger) (*App, error) {
	logger.Info().Str("build", version.String()).Str("node_id", cfg.NodeID).Msg("starting agent")

	m := metrics.New(version.Version)
	st, err := store.Open(cfg.State.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("state store: %w", err)
	}

	registry := inspect.NewRegistry()
	containers := docker.New(cfg.Docker.Socket, cfg.Docker.Enabled, logger)
	if cfg.Docker.Enabled {
		refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := containers.Refresh(refreshCtx); err != nil {
			logger.Warn().Err(err).Msg("docker is not reachable; container enrichment disabled until it recovers")
		}
		cancel()
	}
	local := netinfo.NewLocal()

	defaults := policy.Snapshot{
		Settings: policy.Settings{
			TLSPorts:      toPorts(cfg.Inspection.TLSPorts),
			DNSInspection: cfg.Inspection.DNSEnabled,
			TLSInspection: cfg.Inspection.TLSEnabled,
		},
	}

	var fw policy.Firewall = noopFirewall{}
	var fwManager *firewall.Manager
	if cfg.Firewall.Enabled {
		fwManager, err = firewall.New(firewall.Config{
			TableName: cfg.Firewall.TableName,
			DNSQueue:  cfg.Inspection.DNSQueueNum,
			TLSQueue:  cfg.Inspection.TLSQueueNum,
			BlockLog:  cfg.Firewall.BlockLogGroup,
		})
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("firewall: %w", err)
		}
		fw = fwManager
	} else {
		logger.Warn().Msg("firewall integration is disabled; no traffic will be filtered")
	}

	policyManager := policy.NewManager(fw, st, logger, defaults)
	if err := policyManager.Restore(ctx); err != nil {
		if fwManager != nil {
			_ = fwManager.Close()
		}
		st.Close()
		return nil, fmt.Errorf("restore policy: %w", err)
	}

	collector := events.New(st, events.Config{
		BatchMaxEvents:   cfg.Events.BatchMaxEvents,
		FlushInterval:    cfg.Events.FlushInterval,
		MaxOutboxBatches: cfg.Events.MaxOutboxBatches,
	}, m, logger)

	var tlsCfg *tls.Config
	insecure := !cfg.TLS.Enabled
	if !insecure {
		tlsCfg, err = tlsconfig.Client(cfg.TLS.CAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.ServerName)
		if err != nil {
			if fwManager != nil {
				_ = fwManager.Close()
			}
			st.Close()
			return nil, fmt.Errorf("tls: %w", err)
		}
	}
	hostname, _ := os.Hostname()
	client := transport.New(transport.Config{
		Address:  cfg.CoordinatorAddr,
		TLS:      tlsCfg,
		Insecure: insecure,
		Node: transport.NodeInfo{
			NodeID: cfg.NodeID, Hostname: hostname, AgentVersion: version.Version,
			Addresses: transport.LocalAddresses(),
		},
		HeartbeatInterval: cfg.Events.HeartbeatInterval,
		ReconnectMax:      cfg.Events.ReconnectMax,
		SendTimeout:       cfg.Events.SendTimeout,
	}, transport.StateFuncs{
		AppliedVersion: policyManager.Version,
		Buffered: func() (int64, int64) {
			batches, events, err := st.Counts(context.Background())
			if err != nil {
				return 0, 0
			}
			return batches, events + int64(collector.Buffered())
		},
		Dropped: collector.DroppedTotal,
	}, m, logger)

	outboxSender := sender.New(st, client, sender.Config{
		BatchFetch:  32,
		RetryMin:    cfg.Events.RetryMin,
		RetryMax:    cfg.Events.RetryMax,
		MaxAttempts: cfg.Events.MaxAttempts,
		SendTimeout: cfg.Events.SendTimeout,
	}, collector.Wake(), m, logger)

	m.PolicyVersion.Set(float64(policyManager.Version()))
	m.RulesActive.Set(float64(policyManager.RuleCount()))
	if applied := policyManager.AppliedAt(); !applied.IsZero() {
		m.PolicyApplied.Set(float64(applied.Unix()))
	}

	a := &App{
		cfg: cfg, logger: logger, store: st, metrics: m, registry: registry,
		containers: containers, local: local, firewall: fwManager, policy: policyManager,
		collector: collector, client: client, sender: outboxSender,
		inspectors: cfg.Firewall.Enabled && (cfg.Inspection.DNSEnabled || cfg.Inspection.TLSEnabled),
	}
	return a, nil
}

// Run launches all modules until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error { a.collector.Run(groupCtx); return nil })
	group.Go(func() error { a.sender.Run(groupCtx); return nil })
	group.Go(func() error {
		err := a.client.Run(groupCtx)
		if err != nil && groupCtx.Err() == nil {
			a.logger.Error().Err(err).Msg("coordinator transport stopped")
		}
		return err
	})
	group.Go(func() error { return a.runPolicyLoop(groupCtx) })
	group.Go(func() error {
		return metrics.Serve(groupCtx, a.cfg.Metrics.Addr, a.metrics.Registry, a.logger)
	})
	group.Go(func() error {
		a.containers.Run(groupCtx, a.cfg.Docker.RefreshInterval)
		return nil
	})
	group.Go(func() error { return a.runFlowTracker(groupCtx) })
	group.Go(func() error { return a.runCleanup(groupCtx) })

	if a.inspectors {
		if a.cfg.Inspection.DNSEnabled {
			group.Go(func() error { return a.runDNS(groupCtx) })
		}
		if a.cfg.Inspection.TLSEnabled {
			group.Go(func() error { return a.runTLS(groupCtx) })
		}
		if a.cfg.Firewall.BlockLogGroup > 0 {
			group.Go(func() error { return a.runBlockLog(groupCtx) })
		}
	}

	err := group.Wait()
	if closeErr := a.store.Close(); closeErr != nil {
		a.logger.Warn().Err(closeErr).Msg("state store close failed")
	}
	if a.firewall != nil {
		_ = a.firewall.Close()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (a *App) runPolicyLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-a.client.PolicyUpdates():
			var err error
			for attempt := 1; attempt <= 5; attempt++ {
				applyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = a.policy.Apply(applyCtx, snapshot)
				cancel()
				if err == nil {
					break
				}
				a.metrics.PolicyApplyErrors.Inc()
				a.logger.Error().Err(err).Int("attempt", attempt).Int64("version", snapshot.GetVersion()).
					Msg("policy apply failed")
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(3 * time.Second):
				}
			}
			a.client.SendPolicyAck(snapshot.GetVersion(), err)
			a.metrics.PolicyVersion.Set(float64(a.policy.Version()))
			a.metrics.RulesActive.Set(float64(a.policy.RuleCount()))
			if applied := a.policy.AppliedAt(); !applied.IsZero() {
				a.metrics.PolicyApplied.Set(float64(applied.Unix()))
			}
		}
	}
}

func (a *App) runDNS(ctx context.Context) error {
	inspector := dns.New(a.cfg.Inspection.DNSQueueNum, a.cfg.Inspection.QueueMaxLen,
		a.cfg.Inspection.QueueMaxPacket, a.cfg.Inspection.DNSResponseTimeout,
		a.policy, a.registry, a.containers, a.collector, a.metrics, a.logger)
	if err := inspector.Run(ctx); err != nil {
		a.logger.Error().Err(err).Msg("dns inspector stopped")
	}
	return nil
}

func (a *App) runTLS(ctx context.Context) error {
	inspector := tlsinspect.New(a.cfg.Inspection.TLSQueueNum, a.cfg.Inspection.QueueMaxLen,
		a.cfg.Inspection.QueueMaxPacket, a.cfg.Inspection.BlockedConnTTL,
		a.policy, a.registry, a.containers, a.collector, a.metrics, a.logger)
	if err := inspector.Run(ctx); err != nil {
		a.logger.Error().Err(err).Msg("tls inspector stopped")
	}
	return nil
}

func (a *App) runBlockLog(ctx context.Context) error {
	listener := blocklog.New(a.cfg.Firewall.BlockLogGroup, a.policy, a.registry,
		a.containers, a.collector, a.metrics, a.logger)
	if err := listener.Run(ctx); err != nil {
		a.logger.Error().Err(err).Msg("block log listener stopped")
	}
	return nil
}

func (a *App) runFlowTracker(ctx context.Context) error {
	tracker := flow.New(a.registry, a.containers, a.local, a.collector, a.metrics, a.logger, a.cfg.Inspection.FlowDumpInterval)
	if err := tracker.Run(ctx); err != nil {
		a.logger.Error().Err(err).Msg("flow tracker stopped; connection history will be incomplete")
	}
	return nil
}

func (a *App) runCleanup(ctx context.Context) error {
	registryTicker := time.NewTicker(time.Minute)
	defer registryTicker.Stop()
	localTicker := time.NewTicker(30 * time.Second)
	defer localTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-registryTicker.C:
			a.registry.Cleanup()
		case <-localTicker.C:
			if err := a.local.Refresh(); err != nil {
				a.logger.Debug().Err(err).Msg("failed to refresh local addresses")
			}
		}
	}
}

func toPorts(in []uint32) []uint16 {
	out := make([]uint16, 0, len(in))
	for _, port := range in {
		if port == 0 || port > 65535 {
			continue
		}
		out = append(out, uint16(port))
	}
	return out
}

type noopFirewall struct{}

func (noopFirewall) Apply(context.Context, []firewall.Rule, []uint16, bool, bool) error { return nil }
