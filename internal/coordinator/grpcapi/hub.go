package grpcapi

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/gen/kurwall/v1"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

// SnapshotLoader builds a node-specific policy snapshot.
type SnapshotLoader func(ctx context.Context, nodeID string) (domain.Snapshot, error)

// Hub tracks the active session of every connected agent.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*session
	load     SnapshotLoader
	logger   zerolog.Logger
}

type session struct {
	nodeID string
	out    chan *kurwallv1.CoordinatorResponse
	done   chan struct{}
	once   sync.Once
}

func (s *session) close() {
	s.once.Do(func() { close(s.done) })
}

// enqueue queues a message without blocking. A full buffer closes the session
// so the agent reconnects and receives a complete fresh snapshot.
func (s *session) enqueue(msg *kurwallv1.CoordinatorResponse) bool {
	select {
	case s.out <- msg:
		return true
	case <-s.done:
		return false
	default:
		s.close()
		return false
	}
}

// NewHub creates an empty session hub.
func NewHub(load SnapshotLoader, logger zerolog.Logger) *Hub {
	return &Hub{sessions: map[string]*session{}, load: load, logger: logger}
}

// register installs a new session, closing any previous session for the node.
func (h *Hub) register(nodeID string) *session {
	sess := &session{
		nodeID: nodeID,
		out:    make(chan *kurwallv1.CoordinatorResponse, 32),
		done:   make(chan struct{}),
	}
	h.mu.Lock()
	old := h.sessions[nodeID]
	h.sessions[nodeID] = sess
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	return sess
}

// remove deletes a session only if it is still the current one.
func (h *Hub) remove(nodeID string, sess *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[nodeID] != sess {
		return false
	}
	delete(h.sessions, nodeID)
	sess.close()
	return true
}

// PushPolicy sends fresh snapshots to the given nodes. It runs asynchronously
// because callers are HTTP request handlers.
func (h *Hub) PushPolicy(ctx context.Context, nodeIDs []string) {
	if len(nodeIDs) == 0 {
		return
	}
	for _, nodeID := range nodeIDs {
		h.mu.Lock()
		sess := h.sessions[nodeID]
		h.mu.Unlock()
		if sess == nil {
			continue
		}
		go h.pushOne(nodeID, sess)
	}
}

func (h *Hub) pushOne(nodeID string, sess *session) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := h.load(ctx, nodeID)
	if err != nil {
		h.logger.Error().Err(err).Str("node_id", nodeID).Msg("failed to build policy snapshot for push")
		return
	}
	sess.enqueue(&kurwallv1.CoordinatorResponse{
		Payload: &kurwallv1.CoordinatorResponse_Policy{Policy: SnapshotToProto(snap)},
	})
}

// Close shuts down all sessions.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sess := range h.sessions {
		sess.close()
		delete(h.sessions, id)
	}
}
