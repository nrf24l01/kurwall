// Package app wires and runs the coordinator process.
package app

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/asn"
	"github.com/kurwall/kurwall/internal/coordinator/config"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/enrollca"
	"github.com/kurwall/kurwall/internal/coordinator/grpcapi"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi"
	"github.com/kurwall/kurwall/internal/coordinator/live"
	"github.com/kurwall/kurwall/internal/coordinator/service"
	"github.com/kurwall/kurwall/internal/coordinator/store/clickhouse"
	"github.com/kurwall/kurwall/internal/coordinator/store/postgres"
	"github.com/kurwall/kurwall/internal/platform/tlsconfig"
	"github.com/kurwall/kurwall/internal/platform/version"
)

// App owns all coordinator resources.
type App struct {
	cfg     config.Config
	logger  zerolog.Logger
	pg      *postgres.Store
	ch      *clickhouse.Store
	nodes   *service.Nodes
	events  *service.Events
	asn     *service.ASN
	hub     *grpcapi.Hub
	httpSrv *http.Server
	grpcSrv *grpc.Server
	grpcLis net.Listener
}

// New initializes dependencies and binds listeners so startup fails fast.
func New(ctx context.Context, cfg config.Config, logger zerolog.Logger) (*App, error) {
	logger.Info().Str("build", version.String()).Msg("starting coordinator")

	pg, err := postgres.Open(ctx, cfg.Postgres.DSN(), cfg.Postgres.MaxConns)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	ch, err := clickhouse.Open(ctx, cfg.ClickHouse.Addr, cfg.ClickHouse.Database,
		cfg.ClickHouse.Username, cfg.ClickHouse.Password, cfg.ClickHouse.DialTimeout)
	if err != nil {
		pg.Close()
		return nil, fmt.Errorf("clickhouse: %w", err)
	}

	settings := domain.AgentSettings{
		TLSInspectorPorts: cfg.Agent.TLSInspectorPorts,
		DNSInspection:     cfg.Agent.DNSInspection,
		TLSInspection:     cfg.Agent.TLSInspection,
		BatchMaxEvents:    cfg.Agent.BatchMaxEvents,
		FlushIntervalMS:   uint32(cfg.Agent.FlushInterval.Milliseconds()),
	}

	var nodes *service.Nodes
	liveHub := live.New(logger)
	hub := grpcapi.NewHub(func(ctx context.Context, nodeID string) (domain.Snapshot, error) {
		return nodes.Snapshot(ctx, nodeID)
	}, logger)
	nodes = service.NewNodes(pg, hub, settings)
	var asnResolver *asn.Resolver
	var asnCatalog *asn.Catalog
	if cfg.ASN.Enabled {
		asnResolver = asn.New(cfg.ASN.PrefixURLTemplate, cfg.ASN.HolderURLTemplate, cfg.ASN.RequestTimeout, cfg.ASN.MaxPrefixes, logger)
	}
	if cfg.ASN.CatalogEnabled {
		asnCatalog = asn.NewCatalog(cfg.ASN.CatalogURL, cfg.ASN.CatalogTimeout, logger)
	}
	asnService := service.NewASN(pg, asnResolver, asnCatalog, hub, logger, service.ASNSettings{
		Enabled: cfg.ASN.Enabled, TTL: cfg.ASN.RefreshInterval, Retry: cfg.ASN.RetryInterval,
		Catalog: cfg.ASN.CatalogEnabled, CatalogTTL: cfg.ASN.CatalogRefresh, CatalogRetry: cfg.ASN.CatalogRetry,
	})
	policies := service.NewPolicies(pg, hub, asnService)
	events := service.NewEvents(pg, ch, ch, liveHub, cfg.Ingest.MaxBatchEvents, cfg.Ingest.DedupTimeout)
	authService := service.NewAuth(pg, cfg.Auth.APIToken, logger)

	var deployService *service.Deploy
	var enrollmentCA *x509.Certificate
	var enrollmentKey crypto.Signer
	caFiles := []string{cfg.TLS.CAFile}
	if cfg.TLS.Enabled {
		cert, signer, err := enrollca.LoadOrCreate(cfg.Server.EnrollmentDir)
		if err != nil {
			logger.Warn().Err(err).Msg("node enrollment disabled: could not load the enrollment CA")
		} else {
			enrollmentCA, enrollmentKey = cert, signer
			caFiles = append(caFiles, filepath.Join(cfg.Server.EnrollmentDir, "ca.crt"))
			serverCA, err := tlsconfig.ReadCertificate(cfg.TLS.CAFile)
			if err != nil {
				logger.Warn().Err(err).Msg("node enrollment disabled: could not read the server CA")
			} else {
				deployService = service.NewDeploy(pg, hub, serverCA, enrollmentCA, enrollmentKey, logger)
				logger.Info().Str("dir", cfg.Server.EnrollmentDir).Msg("node enrollment enabled")
			}
		}
	}

	grpcLis, err := net.Listen("tcp", cfg.Server.GRPCAddr)
	if err != nil {
		pg.Close()
		_ = ch.Close()
		return nil, fmt.Errorf("bind grpc listener: %w", err)
	}

	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(16 << 20),
		grpc.MaxSendMsgSize(16 << 20),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 60 * time.Second, Timeout: 20 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		grpc.ChainUnaryInterceptor(grpcapi.TraceUnaryInterceptor(logger)),
		grpc.ChainStreamInterceptor(grpcapi.TraceStreamInterceptor(logger)),
	}
	trustHello := false
	if cfg.TLS.Enabled {
		tlsCfg, err := tlsconfig.Server(caFiles, cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.RequireClientCert)
		if err != nil {
			grpcLis.Close()
			pg.Close()
			_ = ch.Close()
			return nil, fmt.Errorf("tls: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		trustHello = true
		logger.Warn().Msg("gRPC TLS is disabled; agent identity is taken from the hello message")
	}

	grpcSrv := grpc.NewServer(grpcOpts...)
	kurwallv1.RegisterCoordinatorServiceServer(grpcSrv, grpcapi.NewServer(nodes, events, hub, liveHub, logger, trustHello))

	router := httpapi.New(httpapi.Deps{
		Nodes: nodes, Policies: policies, Events: events, ASN: asnService, Live: liveHub,
		Auth: authService, Deploy: deployService, Version: version.Version,
		Settings: settings, WebStaticDir: cfg.Server.WebStaticDir, APIToken: cfg.Auth.APIToken,
		PublicURL: cfg.Server.PublicURL, GRPCPublicAddr: cfg.Server.GRPCPublicAddr,
		GRPCServerName: cfg.Server.GRPCServerName, GRPCAddr: cfg.Server.GRPCAddr, AgentDistDir: cfg.Server.AgentDistDir,
		AgentCAFile: cfg.TLS.CAFile,
		DBHealth:    pg.Health, Logger: logger,
	})
	httpSrv := &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	return &App{
		cfg: cfg, logger: logger, pg: pg, ch: ch, nodes: nodes, events: events, asn: asnService, hub: hub,
		httpSrv: httpSrv, grpcSrv: grpcSrv, grpcLis: grpcLis,
	}, nil
}

// Run serves HTTP and gRPC until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() {
		a.logger.Info().Str("addr", a.cfg.Server.HTTPAddr).Msg("http api listening")
		if err := a.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()
	go func() {
		a.logger.Info().Str("addr", a.cfg.Server.GRPCAddr).Msg("grpc api listening")
		if err := a.grpcSrv.Serve(a.grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("grpc server: %w", err)
		}
	}()

	maintCtx, cancelMaint := context.WithCancel(ctx)
	go a.nodes.ScheduleMaintenance(maintCtx, a.cfg.Agent.OfflineAfter, a.cfg.Ingest.DedupRetention, func(err error) {
		a.logger.Warn().Err(err).Msg("maintenance task failed")
	})
	go a.asn.Run(maintCtx)
	go a.asn.RunCatalog(maintCtx)
	defer cancelMaint()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}
	a.shutdown()
	return runErr
}

func (a *App) shutdown() {
	a.logger.Info().Msg("shutting down coordinator")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.httpSrv.Shutdown(ctx); err != nil {
		a.logger.Warn().Err(err).Msg("http shutdown incomplete")
	}

	a.hub.Close()
	stopped := make(chan struct{})
	go func() {
		a.grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		a.grpcSrv.Stop()
	}

	if err := a.ch.Close(); err != nil {
		a.logger.Warn().Err(err).Msg("clickhouse close failed")
	}
	a.pg.Close()
	a.logger.Info().Msg("coordinator stopped")
}
