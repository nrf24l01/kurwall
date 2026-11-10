// Package service implements coordinator use cases. It orchestrates the
// repository, event storage and live policy push without depending on HTTP or
// gRPC types.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

// Notifier pushes fresh policy snapshots to connected agents.
type Notifier interface {
	PushPolicy(ctx context.Context, nodeIDs []string)
}

// Nodes handles node registration, heartbeats and policy assignment.
type Nodes struct {
	repo     domain.Repository
	notifier Notifier
	settings domain.AgentSettings
}

// NewNodes builds the node service.
func NewNodes(repo domain.Repository, notifier Notifier, settings domain.AgentSettings) *Nodes {
	return &Nodes{repo: repo, notifier: notifier, settings: settings}
}

// Register upserts the node reported by a connecting agent.
func (s *Nodes) Register(ctx context.Context, hello domain.Hello) (domain.Node, error) {
	now := time.Now().UTC()
	return s.repo.UpsertNode(ctx, hello, domain.NodeStatusOnline, now)
}

// Touch records a live heartbeat.
func (s *Nodes) Touch(ctx context.Context, nodeID string) error {
	return s.repo.SetNodeStatus(ctx, nodeID, domain.NodeStatusOnline, ptr(time.Now().UTC()))
}

// SetPolicyVersion stores the snapshot version acknowledged by the agent.
func (s *Nodes) SetPolicyVersion(ctx context.Context, nodeID string, version int64) error {
	return s.repo.SetNodePolicyVersion(ctx, nodeID, version)
}

// Disconnect marks a node offline, keeping its last-seen timestamp.
func (s *Nodes) Disconnect(ctx context.Context, nodeID string) error {
	return s.repo.SetNodeStatus(ctx, nodeID, domain.NodeStatusOffline, nil)
}

// List returns all nodes.
func (s *Nodes) List(ctx context.Context) ([]domain.Node, error) {
	return s.repo.ListNodes(ctx)
}

// Get returns one node.
func (s *Nodes) Get(ctx context.Context, id string) (domain.Node, error) {
	return s.repo.GetNode(ctx, id)
}

// Update renames a node or replaces its labels.
func (s *Nodes) Update(ctx context.Context, id string, update domain.NodeUpdate) (domain.Node, error) {
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if name == "" || len(name) > 128 {
			return domain.Node{}, apperr.Validation("name must be between 1 and 128 characters")
		}
		update.Name = &name
	}
	if update.Labels != nil {
		if len(*update.Labels) > 32 {
			return domain.Node{}, apperr.Validation("at most 32 labels are allowed")
		}
		for k, v := range *update.Labels {
			if len(k) == 0 || len(k) > 64 || len(v) > 128 {
				return domain.Node{}, apperr.Validation("label keys must be 1-64 and values at most 128 characters")
			}
		}
	}
	return s.repo.UpdateNode(ctx, id, update)
}

// AssignPolicies replaces the policy set of a node and pushes a new snapshot.
func (s *Nodes) AssignPolicies(ctx context.Context, nodeID string, policyIDs []string, actor string) (domain.Node, error) {
	if _, err := s.repo.GetNode(ctx, nodeID); err != nil {
		return domain.Node{}, err
	}
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(policyIDs))
	for _, policyID := range policyIDs {
		policyID = strings.TrimSpace(policyID)
		if policyID == "" {
			continue
		}
		if _, ok := seen[policyID]; ok {
			continue
		}
		seen[policyID] = struct{}{}
		cleaned = append(cleaned, policyID)
	}
	if err := s.repo.SetNodePolicies(ctx, nodeID, cleaned); err != nil {
		return domain.Node{}, err
	}
	if _, err := s.repo.CreateRevision(ctx, actor); err != nil {
		return domain.Node{}, err
	}
	if s.notifier != nil {
		s.notifier.PushPolicy(ctx, []string{nodeID})
	}
	return s.repo.GetNode(ctx, nodeID)
}

// Snapshot returns the effective policy for a node with the configured agent
// settings attached.
func (s *Nodes) Snapshot(ctx context.Context, nodeID string) (domain.Snapshot, error) {
	snap, err := s.repo.SnapshotForNode(ctx, nodeID)
	if err != nil {
		return domain.Snapshot{}, err
	}
	// Expand ASN rules for the agent. The prefixes come from the durable cache,
	// so a coordinator restart or resolver outage does not disarm them.
	for i := range snap.Rules {
		if snap.Rules[i].Kind != domain.RuleKindASN {
			continue
		}
		asn, err := ruleutil.ParseASN(snap.Rules[i].Value)
		if err != nil {
			continue
		}
		cache, found, err := s.repo.GetASNCache(ctx, asn)
		if err != nil || !found {
			continue
		}
		snap.Rules[i].Prefixes = cache.Prefixes
		snap.Rules[i].PrefixCount = len(cache.Prefixes)
	}
	snap.Settings = s.settings
	snap.GeneratedAt = time.Now().UTC()
	return snap, nil
}

// ScheduleMaintenance flips stale nodes offline and cleans dedup records.
func (s *Nodes) ScheduleMaintenance(ctx context.Context, offlineAfter, dedupRetention time.Duration, logger func(error)) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := s.repo.MarkStaleOffline(ctx, now.Add(-offlineAfter)); err != nil {
				logger(err)
			}
			if _, err := s.repo.CleanupBatches(ctx, now.Add(-dedupRetention)); err != nil {
				logger(err)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

// ValidateNodeID ensures identity strings are safe for URLs and logs.
func ValidateNodeID(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\n\r\t") {
		return fmt.Errorf("invalid node id")
	}
	return nil
}
