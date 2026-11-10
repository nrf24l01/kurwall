// Package transport maintains the persistent mutually-authenticated gRPC
// stream to the coordinator, routes acknowledgements and receives policies.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/agent/metrics"
)

// NodeInfo describes the agent to the coordinator.
type NodeInfo struct {
	NodeID       string
	Hostname     string
	AgentVersion string
	Addresses    []string
}

// StateFuncs exposes local state for heartbeats.
type StateFuncs struct {
	AppliedVersion func() int64
	Buffered       func() (batches, events int64)
	Dropped        func() uint64
}

// Config controls the gRPC transport.
type Config struct {
	Address           string
	TLS               *tls.Config // nil means plaintext (development only)
	Insecure          bool
	Node              NodeInfo
	HeartbeatInterval time.Duration
	ReconnectMax      time.Duration
	SendTimeout       time.Duration
}

// SendError reports a rejected or failed batch.
type SendError struct {
	Err       error
	Retryable bool
}

func (e *SendError) Error() string { return e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// IsRetryable reports whether a send error can be retried.
func IsRetryable(err error) bool {
	var sendErr *SendError
	if errors.As(err, &sendErr) {
		return sendErr.Retryable
	}
	return true
}

// Client owns the stream lifecycle and request/ack correlation.
type Client struct {
	cfg     Config
	logger  zerolog.Logger
	state   StateFuncs
	metrics *metrics.Metrics

	conn     *grpc.ClientConn
	sendCh   chan *kurwallv1.AgentRequest
	policyCh chan *kurwallv1.PolicySnapshot

	mu        sync.Mutex
	waiters   map[string]chan *kurwallv1.EventAck
	readyCh   chan struct{}
	connected bool
}

// New builds a client.
func New(cfg Config, state StateFuncs, m *metrics.Metrics, logger zerolog.Logger) *Client {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 15 * time.Second
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 60 * time.Second
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 30 * time.Second
	}
	return &Client{
		cfg: cfg, logger: logger, state: state, metrics: m,
		sendCh:   make(chan *kurwallv1.AgentRequest, 256),
		policyCh: make(chan *kurwallv1.PolicySnapshot, 16),
		waiters:  map[string]chan *kurwallv1.EventAck{},
		readyCh:  make(chan struct{}),
	}
}

// PolicyUpdates returns the channel of snapshots that must be applied.
func (c *Client) PolicyUpdates() <-chan *kurwallv1.PolicySnapshot { return c.policyCh }

// Run maintains the connection until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20), grpc.MaxCallSendMsgSize(16<<20)),
	}
	if c.cfg.Insecure || c.cfg.TLS == nil {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(c.cfg.TLS)))
	}
	conn, err := grpc.NewClient(c.cfg.Address, opts...)
	if err != nil {
		return fmt.Errorf("create grpc client: %w", err)
	}
	c.conn = conn
	defer conn.Close()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		connectedAt := time.Now()
		err := c.session(ctx, conn)
		c.setConnected(false)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(connectedAt) > 30*time.Second {
			backoff = time.Second
		}
		c.metrics.Reconnects.Inc()
		c.logger.Warn().Err(err).Dur("backoff", backoff).Msg("coordinator stream ended; reconnecting")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter(backoff)):
		}
		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

func (c *Client) session(ctx context.Context, conn *grpc.ClientConn) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := kurwallv1.NewCoordinatorServiceClient(conn).Connect(streamCtx)
	if err != nil {
		return err
	}
	hello := &kurwallv1.AgentRequest{Payload: &kurwallv1.AgentRequest_Hello{Hello: &kurwallv1.AgentHello{
		NodeId: c.cfg.Node.NodeID, Hostname: c.cfg.Node.Hostname, AgentVersion: c.cfg.Node.AgentVersion,
		Os: runtime.GOOS, Kernel: kernelRelease(), Addresses: c.cfg.Node.Addresses,
	}}}
	if err := stream.Send(hello); err != nil {
		return err
	}

	writeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case msg := <-c.sendCh:
				if err := stream.Send(msg); err != nil {
					writeErr <- err
					cancel()
					return
				}
			}
		}
	}()

	go c.heartbeat(streamCtx)
	c.logger.Info().Str("addr", c.cfg.Address).Str("node_id", c.cfg.Node.NodeID).Msg("connecting to coordinator")

	for {
		select {
		case <-streamCtx.Done():
			return streamCtx.Err()
		case err := <-writeErr:
			return err
		default:
		}
		resp, err := stream.Recv()
		if err != nil {
			return err
		}
		c.handleResponse(streamCtx, resp)
	}
}

func (c *Client) handleResponse(ctx context.Context, resp *kurwallv1.CoordinatorResponse) {
	switch payload := resp.Payload.(type) {
	case *kurwallv1.CoordinatorResponse_Welcome:
		c.logger.Info().Str("session_id", payload.Welcome.GetSessionId()).Msg("coordinator stream established")
		c.setConnected(true)
	case *kurwallv1.CoordinatorResponse_Policy:
		select {
		case c.policyCh <- payload.Policy:
		case <-ctx.Done():
		}
	case *kurwallv1.CoordinatorResponse_Ack:
		ack := payload.Ack
		c.mu.Lock()
		waiter, ok := c.waiters[ack.GetBatchId()]
		c.mu.Unlock()
		if ok {
			select {
			case waiter <- ack:
			default:
			}
		}
	case *kurwallv1.CoordinatorResponse_Ping:
		// Server ping carries no action; the heartbeat keeps the stream alive.
	}
}

func (c *Client) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := &kurwallv1.AgentHeartbeat{SentAtUnixNano: time.Now().UnixNano(), AppliedPolicyVersion: c.state.AppliedVersion()}
			if c.state.Buffered != nil {
				_, events := c.state.Buffered()
				hb.BufferedEvents = uint64(max64(events, 0))
			}
			if c.state.Dropped != nil {
				hb.DroppedEvents = c.state.Dropped()
			}
			select {
			case c.sendCh <- &kurwallv1.AgentRequest{Payload: &kurwallv1.AgentRequest_Heartbeat{Heartbeat: hb}}:
			default:
				c.logger.Warn().Msg("send queue full; heartbeat dropped")
			}
		}
	}
}

// SendBatch transmits a batch and waits for its acknowledgement.
func (c *Client) SendBatch(ctx context.Context, batch *kurwallv1.EventBatch) (bool, error) {
	if batch == nil || batch.GetBatchId() == "" {
		return false, &SendError{Err: errors.New("batch is empty"), Retryable: false}
	}
	if !c.isConnected() {
		return false, &SendError{Err: errors.New("coordinator is not connected"), Retryable: true}
	}
	waiter := make(chan *kurwallv1.EventAck, 1)
	c.mu.Lock()
	c.waiters[batch.GetBatchId()] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, batch.GetBatchId())
		c.mu.Unlock()
	}()

	req := &kurwallv1.AgentRequest{Payload: &kurwallv1.AgentRequest_Events{Events: batch}}
	select {
	case c.sendCh <- req:
	case <-ctx.Done():
		return false, &SendError{Err: ctx.Err(), Retryable: true}
	}

	timeout := time.NewTimer(c.cfg.SendTimeout)
	defer timeout.Stop()
	select {
	case ack := <-waiter:
		if ack.GetSuccess() {
			return ack.GetDuplicate(), nil
		}
		message := ack.GetError()
		if message == "" {
			message = "coordinator rejected batch"
		}
		return false, &SendError{Err: errors.New(message), Retryable: ack.GetRetryable()}
	case <-timeout.C:
		return false, &SendError{Err: errors.New("acknowledgement timed out"), Retryable: true}
	case <-ctx.Done():
		return false, &SendError{Err: ctx.Err(), Retryable: true}
	}
}

// SendPolicyAck reports the result of applying a snapshot. It is best-effort
// because a reconnect causes the coordinator to push the snapshot again.
func (c *Client) SendPolicyAck(version int64, applyErr error) {
	ack := &kurwallv1.PolicyAck{Version: version, Success: applyErr == nil}
	if applyErr != nil {
		ack.Error = applyErr.Error()
		if len(ack.Error) > 300 {
			ack.Error = ack.Error[:300]
		}
	}
	select {
	case c.sendCh <- &kurwallv1.AgentRequest{Payload: &kurwallv1.AgentRequest_PolicyAck{PolicyAck: ack}}:
	default:
		c.logger.Warn().Msg("send queue full; policy acknowledgement dropped")
	}
}

// WaitReady blocks until the stream is established or ctx is cancelled.
func (c *Client) WaitReady(ctx context.Context) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	ch := c.readyCh
	c.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsConnected reports stream state.
func (c *Client) IsConnected() bool { return c.isConnected() }

func (c *Client) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Client) setConnected(connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected == connected {
		return
	}
	c.connected = connected
	if connected {
		close(c.readyCh)
		c.metrics.Connected.Set(1)
		return
	}
	c.readyCh = make(chan struct{})
	c.metrics.Connected.Set(0)
	for batchID, waiter := range c.waiters {
		select {
		case waiter <- &kurwallv1.EventAck{BatchId: batchID, Success: false, Error: "connection lost", Retryable: true}:
		default:
		}
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	delta := d / 4
	return d - delta + time.Duration(rand.Int63n(int64(2*delta)+1))
}

func kernelRelease() string {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// LocalAddresses returns non-loopback interface addresses.
func LocalAddresses() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil || ip.IsLoopback() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}
