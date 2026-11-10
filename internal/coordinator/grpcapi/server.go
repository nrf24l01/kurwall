package grpcapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/live"
	"github.com/kurwall/kurwall/internal/coordinator/service"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// Server implements the CoordinatorService gRPC API.
type Server struct {
	kurwallv1.UnimplementedCoordinatorServiceServer

	nodes      *service.Nodes
	events     *service.Events
	hub        *Hub
	live       *live.Hub
	logger     zerolog.Logger
	trustHello bool
}

// NewServer builds the gRPC service. trustHello must only be enabled when TLS
// is disabled for local development, where no client certificate identity
// exists.
func NewServer(nodes *service.Nodes, events *service.Events, hub *Hub, liveHub *live.Hub, logger zerolog.Logger, trustHello bool) *Server {
	return &Server{nodes: nodes, events: events, hub: hub, live: liveHub, logger: logger, trustHello: trustHello}
}

type recvResult struct {
	req *kurwallv1.AgentRequest
	err error
}

// Connect handles one persistent agent stream for its whole lifetime.
func (s *Server) Connect(stream kurwallv1.CoordinatorService_ConnectServer) error {
	ctx := stream.Context()
	logger := *zerolog.Ctx(ctx)

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be hello")
	}

	nodeID, err := s.nodeIdentity(ctx, hello)
	if err != nil {
		return err
	}
	if err := service.ValidateNodeID(nodeID); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	hello.NodeId = nodeID
	logger = logger.With().Str("node_id", nodeID).Str("hostname", hello.Hostname).Logger()
	ctx = logger.WithContext(ctx)

	if _, err := s.nodes.Register(ctx, domain.Hello{
		NodeID: nodeID, Hostname: hello.Hostname, AgentVersion: hello.AgentVersion,
		OS: hello.GetOs(), Kernel: hello.Kernel, Addresses: hello.Addresses,
	}); err != nil {
		logger.Error().Err(err).Msg("failed to register agent")
		return status.Error(codes.Internal, "failed to register agent")
	}
	if s.live != nil {
		s.live.PublishNodeStatus(nodeID, "online")
	}

	sess := s.hub.register(nodeID)
	defer func() {
		current := s.hub.remove(nodeID, sess)
		sess.close()
		if current {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.nodes.Disconnect(stopCtx, nodeID); err != nil {
				logger.Error().Err(err).Msg("failed to mark node offline")
			}
			if s.live != nil {
				s.live.PublishNodeStatus(nodeID, "offline")
			}
			logger.Info().Msg("agent disconnected")
		}
	}()

	snapshot, err := s.nodes.Snapshot(ctx, nodeID)
	if err != nil {
		logger.Error().Err(err).Msg("failed to build initial policy snapshot")
		return status.Error(codes.Unavailable, "policy snapshot unavailable")
	}

	sess.enqueue(&kurwallv1.CoordinatorResponse{
		Payload: &kurwallv1.CoordinatorResponse_Welcome{Welcome: &kurwallv1.ServerWelcome{
			NodeId:             nodeID,
			SessionId:          nodeID + "-" + time.Now().UTC().Format("20060102T150405"),
			ServerTimeUnixNano: time.Now().UnixNano(),
		}},
	})
	sess.enqueue(&kurwallv1.CoordinatorResponse{
		Payload: &kurwallv1.CoordinatorResponse_Policy{Policy: SnapshotToProto(snapshot)},
	})
	logger.Info().Int64("policy_version", snapshot.Version).Msg("agent connected")

	writeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case msg := <-sess.out:
				if err := stream.Send(msg); err != nil {
					writeErr <- err
					return
				}
			case <-sess.done:
				writeErr <- nil
				return
			case <-ctx.Done():
				writeErr <- ctx.Err()
				return
			}
		}
	}()

	recvCh := make(chan recvResult)
	go func() {
		for {
			req, err := stream.Recv()
			select {
			case recvCh <- recvResult{req: req, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-writeErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Debug().Err(err).Msg("agent stream write ended")
			}
			return nil
		case result := <-recvCh:
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					logger.Debug().Err(result.err).Msg("agent stream read ended")
				}
				return nil
			}
			s.handle(ctx, nodeID, sess, result.req, logger)
		}
	}
}

func (s *Server) handle(ctx context.Context, nodeID string, sess *session, req *kurwallv1.AgentRequest, logger zerolog.Logger) {
	switch payload := req.Payload.(type) {
	case *kurwallv1.AgentRequest_Hello:
		logger.Debug().Msg("duplicate hello ignored")
	case *kurwallv1.AgentRequest_Heartbeat:
		hb := payload.Heartbeat
		if err := s.nodes.Touch(ctx, nodeID); err != nil {
			logger.Warn().Err(err).Msg("failed to record heartbeat")
		}
		if v := hb.GetAppliedPolicyVersion(); v > 0 {
			if err := s.nodes.SetPolicyVersion(ctx, nodeID, v); err != nil {
				logger.Warn().Err(err).Msg("failed to record applied policy version")
			}
		}
	case *kurwallv1.AgentRequest_PolicyAck:
		ack := payload.PolicyAck
		if ack.GetSuccess() {
			if err := s.nodes.SetPolicyVersion(ctx, nodeID, ack.GetVersion()); err != nil {
				logger.Warn().Err(err).Msg("failed to record policy acknowledgement")
			}
			logger.Info().Int64("policy_version", ack.GetVersion()).Msg("agent applied policy")
		} else {
			logger.Error().Str("error", ack.GetError()).Int64("policy_version", ack.GetVersion()).Msg("agent failed to apply policy")
		}
	case *kurwallv1.AgentRequest_Events:
		batch := payload.Events
		duplicate, err := s.events.Ingest(ctx, nodeID, batch.GetBatchId(), batch.GetEvents())
		ack := &kurwallv1.EventAck{BatchId: batch.GetBatchId(), Success: err == nil, Duplicate: duplicate}
		if err != nil {
			ack.Retryable = apperr.IsRetryable(err)
			if appErr, ok := apperr.As(err); ok {
				ack.Error = appErr.Message
				logger.Warn().Err(err).Str("batch_id", batch.GetBatchId()).Str("code", appErr.Code).
					Int("events", len(batch.GetEvents())).Msg("event batch rejected")
			} else {
				ack.Error = "internal error"
				logger.Error().Err(err).Str("batch_id", batch.GetBatchId()).Msg("event batch failed")
			}
		} else if duplicate {
			ack.Duplicate = true
		}
		if !sess.enqueue(&kurwallv1.CoordinatorResponse{
			Payload: &kurwallv1.CoordinatorResponse_Ack{Ack: ack},
		}) {
			logger.Warn().Msg("failed to enqueue event acknowledgement")
		}
	}
}

func (s *Server) nodeIdentity(ctx context.Context, hello *kurwallv1.AgentHello) (string, error) {
	if cn := certificateCN(ctx); cn != "" {
		if hello.GetNodeId() != "" && hello.GetNodeId() != cn {
			zerolog.Ctx(ctx).Warn().Str("certificate_cn", cn).Str("hello_node_id", hello.GetNodeId()).
				Msg("ignoring hello node id that does not match the client certificate")
		}
		return cn, nil
	}
	if s.trustHello {
		nodeID := strings.TrimSpace(hello.GetNodeId())
		if nodeID == "" {
			return "", status.Error(codes.Unauthenticated, "node id is required")
		}
		return nodeID, nil
	}
	return "", status.Error(codes.Unauthenticated, "client certificate is required")
}

func certificateCN(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return ""
	}
	return strings.TrimSpace(info.State.VerifiedChains[0][0].Subject.CommonName)
}
