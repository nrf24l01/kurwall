// Package metrics exposes Prometheus metrics for agent health and traffic.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// Metrics holds all agent Prometheus instruments.
type Metrics struct {
	Registry *prometheus.Registry

	EventsRecorded *prometheus.CounterVec
	EventsDropped  *prometheus.CounterVec
	Blocks         *prometheus.CounterVec
	DNSQueries     prometheus.Counter
	Flows          prometheus.Counter
	Bytes          *prometheus.CounterVec
	Packets        *prometheus.CounterVec

	OutboxBatches prometheus.Gauge
	OutboxEvents  prometheus.Gauge
	Connected     prometheus.Gauge
	PolicyVersion prometheus.Gauge
	PolicyApplied prometheus.Gauge
	RulesActive   prometheus.Gauge

	Reconnects        prometheus.Counter
	PolicyApplyErrors prometheus.Counter
	QueueErrors       *prometheus.CounterVec
	QueuePackets      *prometheus.CounterVec
	BlockLogErrors    prometheus.Counter
	DockerErrors      prometheus.Counter
	FlowDumpErrors    prometheus.Counter
	InspectorDrops    *prometheus.CounterVec
}

// New creates and registers the agent metrics.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		EventsRecorded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_events_recorded_total", Help: "Network events recorded into the local outbox.",
		}, []string{"kind"}),
		EventsDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_events_dropped_total", Help: "Network events dropped before persistence.",
		}, []string{"kind"}),
		Blocks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_blocks_total", Help: "Blocking decisions emitted.",
		}, []string{"kind"}),
		DNSQueries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_dns_queries_total", Help: "DNS queries observed.",
		}),
		Flows: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_flows_total", Help: "Completed connections observed.",
		}),
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_flow_bytes_total", Help: "Bytes observed by direction.",
		}, []string{"direction"}),
		Packets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_flow_packets_total", Help: "Packets observed by direction.",
		}, []string{"direction"}),
		OutboxBatches: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_outbox_batches", Help: "Event batches waiting for acknowledgement.",
		}),
		OutboxEvents: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_outbox_events", Help: "Events waiting for acknowledgement.",
		}),
		Connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_coordinator_connected", Help: "1 when the gRPC stream is connected.",
		}),
		PolicyVersion: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_policy_version", Help: "Applied policy revision.",
		}),
		PolicyApplied: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_policy_applied_timestamp_seconds", Help: "Unix time of the last applied policy.",
		}),
		RulesActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kurwall_agent_rules_active", Help: "Active filtering rules.",
		}),
		Reconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_grpc_reconnects_total", Help: "Coordinator stream reconnect attempts.",
		}),
		PolicyApplyErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_policy_apply_errors_total", Help: "Failed policy applications.",
		}),
		QueueErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_nfqueue_errors_total", Help: "NFQUEUE errors by inspector.",
		}, []string{"inspector"}),
		QueuePackets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_nfqueue_packets_total", Help: "Packets received from NFQUEUE by inspector.",
		}, []string{"inspector"}),
		BlockLogErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_nflog_errors_total", Help: "NFLOG listener errors.",
		}),
		DockerErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_docker_refresh_errors_total", Help: "Docker metadata refresh errors.",
		}),
		FlowDumpErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kurwall_agent_flow_snapshot_errors_total", Help: "Conntrack accounting snapshot errors.",
		}),
		InspectorDrops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kurwall_agent_inspector_drops_total", Help: "Packets dropped by userspace inspectors.",
		}, []string{"inspector"}),
	}
	reg.MustRegister(
		m.EventsRecorded, m.EventsDropped, m.Blocks, m.DNSQueries, m.Flows,
		m.Bytes, m.Packets, m.OutboxBatches, m.OutboxEvents, m.Connected,
		m.PolicyVersion, m.PolicyApplied, m.RulesActive, m.Reconnects,
		m.PolicyApplyErrors, m.QueueErrors, m.QueuePackets, m.BlockLogErrors, m.DockerErrors, m.FlowDumpErrors, m.InspectorDrops,
	)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	_ = reg.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "kurwall_agent_build_info", Help: "Agent build information.",
		ConstLabels: prometheus.Labels{"version": version},
	}, func() float64 { return 1 }))
	return m
}

// Serve exposes /metrics until ctx is cancelled.
func Serve(ctx context.Context, addr string, registry *prometheus.Registry, logger zerolog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", addr).Msg("metrics listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("metrics server: %w", err)
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
