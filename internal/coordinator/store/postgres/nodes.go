package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

const nodeColumns = `n.id, n.name, n.hostname, n.agent_version, n.os, n.kernel,
	n.addresses, n.status, n.last_seen_at, n.labels, n.policy_version, n.created_at, n.updated_at,
	COALESCE(array_agg(np.policy_id) FILTER (WHERE np.policy_id IS NOT NULL), '{}')`

const nodeGroupBy = `GROUP BY n.id`

// UpsertNode auto-registers an agent identified by its mTLS identity.
func (s *Store) UpsertNode(ctx context.Context, hello domain.Hello, status string, at time.Time) (domain.Node, error) {
	if hello.NodeID == "" {
		return domain.Node{}, apperr.Validation("node identity is required")
	}
	addresses := hello.Addresses
	if addresses == nil {
		addresses = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO nodes (id, name, hostname, agent_version, os, kernel, addresses, status, last_seen_at)
		VALUES ($1, $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			agent_version = EXCLUDED.agent_version,
			os = EXCLUDED.os,
			kernel = EXCLUDED.kernel,
			addresses = EXCLUDED.addresses,
			status = EXCLUDED.status,
			last_seen_at = EXCLUDED.last_seen_at,
			updated_at = now()
		RETURNING id`,
		hello.NodeID, hello.Hostname, hello.AgentVersion, hello.OS, hello.Kernel, addresses, status, at)
	var id string
	if err := row.Scan(&id); err != nil {
		return domain.Node{}, apperr.Internal("upsert node", err)
	}
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return domain.Node{}, err
	}
	if hello.Hostname != "" && node.Name == node.ID {
		// Use the reported hostname as the display name until an operator
		// renames the node explicitly.
		if _, err := s.pool.Exec(ctx, `UPDATE nodes SET name = $2 WHERE id = $1 AND name = id`, id, hello.Hostname); err == nil {
			node.Name = hello.Hostname
		}
	}
	return node, nil
}

// ListNodes returns all registered nodes with their assigned policies.
func (s *Store) ListNodes(ctx context.Context) ([]domain.Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeColumns+`
		FROM nodes n LEFT JOIN node_policies np ON np.node_id = n.id
		`+nodeGroupBy+` ORDER BY n.name`)
	if err != nil {
		return nil, apperr.Internal("list nodes", err)
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, apperr.Internal("scan nodes", err)
	}
	return nodes, nil
}

// GetNode returns one node.
func (s *Store) GetNode(ctx context.Context, id string) (domain.Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeColumns+`
		FROM nodes n LEFT JOIN node_policies np ON np.node_id = n.id
		WHERE n.id = $1 `+nodeGroupBy, id)
	if err != nil {
		return domain.Node{}, apperr.Internal("get node", err)
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return domain.Node{}, apperr.Internal("scan node", err)
	}
	if len(nodes) == 0 {
		return domain.Node{}, apperr.NotFound("NODE_NOT_FOUND", "node not found")
	}
	return nodes[0], nil
}

func scanNodes(rows pgx.Rows) ([]domain.Node, error) {
	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		var labels []byte
		if err := rows.Scan(&n.ID, &n.Name, &n.Hostname, &n.AgentVersion, &n.OS, &n.Kernel,
			&n.Addresses, &n.Status, &n.LastSeenAt, &labels, &n.PolicyVersion, &n.CreatedAt, &n.UpdatedAt, &n.PolicyIDs); err != nil {
			return nil, err
		}
		n.Labels = map[string]string{}
		if len(labels) > 0 {
			_ = json.Unmarshal(labels, &n.Labels)
		}
		if n.PolicyIDs == nil {
			n.PolicyIDs = []string{}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNode renames a node and/or replaces its labels.
func (s *Store) UpdateNode(ctx context.Context, id string, update domain.NodeUpdate) (domain.Node, error) {
	var labels []byte
	if update.Labels != nil {
		encoded, err := json.Marshal(*update.Labels)
		if err != nil {
			return domain.Node{}, apperr.Validation("labels must be a JSON object")
		}
		labels = encoded
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nodes SET
			name = COALESCE($2, name),
			labels = COALESCE($3::jsonb, labels),
			updated_at = now()
		WHERE id = $1`,
		id, update.Name, labels)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Node{}, apperr.Conflict("NODE_NAME_TAKEN", "another node already uses this name")
		}
		return domain.Node{}, apperr.Internal("update node", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Node{}, apperr.NotFound("NODE_NOT_FOUND", "node not found")
	}
	return s.GetNode(ctx, id)
}

// SetNodeStatus records connectivity changes.
func (s *Store) SetNodeStatus(ctx context.Context, id, status string, lastSeen *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET status = $2, last_seen_at = COALESCE($3, last_seen_at), updated_at = now() WHERE id = $1`, id, status, lastSeen)
	if err != nil {
		return apperr.Internal("set node status", err)
	}
	return nil
}

// SetNodePolicyVersion records the last policy snapshot acknowledged by an agent.
func (s *Store) SetNodePolicyVersion(ctx context.Context, id string, version int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET policy_version = $2, updated_at = now() WHERE id = $1`, id, version)
	if err != nil {
		return apperr.Internal("set node policy version", err)
	}
	return nil
}

// MarkStaleOffline flips nodes that stopped heartbeating.
func (s *Store) MarkStaleOffline(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'offline', updated_at = now()
		WHERE status = 'online' AND (last_seen_at IS NULL OR last_seen_at < $1)`, before)
	if err != nil {
		return 0, apperr.Internal("mark stale nodes offline", err)
	}
	return tag.RowsAffected(), nil
}

// SetNodePolicies replaces the policy assignments of a node.
func (s *Store) SetNodePolicies(ctx context.Context, nodeID string, policyIDs []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return apperr.Internal("begin policy assignment", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM node_policies WHERE node_id = $1`, nodeID); err != nil {
		return apperr.Internal("clear policy assignment", err)
	}
	for _, policyID := range policyIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO node_policies (node_id, policy_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, nodeID, policyID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return apperr.Validation(fmt.Sprintf("policy %s does not exist", policyID))
			}
			return apperr.Internal("assign policy", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Internal("commit policy assignment", err)
	}
	return nil
}

// NodePolicyIDs returns the policies assigned to a node.
func (s *Store) NodePolicyIDs(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT policy_id FROM node_policies WHERE node_id = $1 ORDER BY policy_id`, nodeID)
	if err != nil {
		return nil, apperr.Internal("list node policies", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Internal("scan node policy", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ActiveNodeIDs returns all known node IDs.
func (s *Store) ActiveNodeIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM nodes ORDER BY id`)
	if err != nil {
		return nil, apperr.Internal("list node ids", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Internal("scan node id", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
