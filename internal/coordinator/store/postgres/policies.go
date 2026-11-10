package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/id"
)

const policyColumns = `p.id, p.name, p.description, p.enabled, p.created_at, p.updated_at,
	(SELECT count(*) FROM rules r WHERE r.policy_id = p.id),
	COALESCE((
		SELECT jsonb_agg(jsonb_build_object(
			'id', n.id, 'name', n.name, 'status', n.status,
			'agentVersion', n.agent_version, 'policyVersion', n.policy_version
		) ORDER BY n.name)
		FROM node_policies np JOIN nodes n ON n.id = np.node_id
		WHERE np.policy_id = p.id
	), '[]'::jsonb)`

// ListPolicies returns all policies with their rule counts and assigned nodes.
func (s *Store) ListPolicies(ctx context.Context) ([]domain.Policy, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+policyColumns+` FROM policies p ORDER BY p.name`)
	if err != nil {
		return nil, apperr.Internal("list policies", err)
	}
	defer rows.Close()
	var out []domain.Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPolicy returns one policy.
func (s *Store) GetPolicy(ctx context.Context, policyID string) (domain.Policy, error) {
	p, err := scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies p WHERE p.id = $1`, policyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, apperr.NotFound("POLICY_NOT_FOUND", "policy not found")
	}
	if err != nil {
		return domain.Policy{}, apperr.Internal("get policy", err)
	}
	return p, nil
}

type policyScanner interface {
	Scan(dest ...any) error
}

func scanPolicy(row policyScanner) (domain.Policy, error) {
	var p domain.Policy
	var nodes []byte
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.RuleCount, &nodes); err != nil {
		return domain.Policy{}, err
	}
	if err := json.Unmarshal(nodes, &p.Nodes); err != nil {
		return domain.Policy{}, apperr.Internal("decode policy nodes", err)
	}
	if p.Nodes == nil {
		p.Nodes = []domain.PolicyNode{}
	}
	return p, nil
}

// CreatePolicy inserts a policy.
func (s *Store) CreatePolicy(ctx context.Context, input domain.PolicyInput) (domain.Policy, error) {
	id := id.New()
	_, err := s.pool.Exec(ctx, `INSERT INTO policies (id, name, description, enabled) VALUES ($1, $2, $3, $4)`,
		id, input.Name, input.Description, input.Enabled)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Policy{}, apperr.Conflict("POLICY_NAME_TAKEN", "a policy with this name already exists")
		}
		return domain.Policy{}, apperr.Internal("create policy", err)
	}
	return s.GetPolicy(ctx, id)
}

// UpdatePolicy updates a policy.
func (s *Store) UpdatePolicy(ctx context.Context, policyID string, input domain.PolicyInput) (domain.Policy, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE policies SET name = $2, description = $3, enabled = $4, updated_at = now() WHERE id = $1`,
		policyID, input.Name, input.Description, input.Enabled)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Policy{}, apperr.Conflict("POLICY_NAME_TAKEN", "a policy with this name already exists")
		}
		return domain.Policy{}, apperr.Internal("update policy", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Policy{}, apperr.NotFound("POLICY_NOT_FOUND", "policy not found")
	}
	return s.GetPolicy(ctx, policyID)
}

// DeletePolicy removes a policy and its rules; assignments cascade.
func (s *Store) DeletePolicy(ctx context.Context, policyID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM policies WHERE id = $1`, policyID)
	if err != nil {
		return apperr.Internal("delete policy", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("POLICY_NOT_FOUND", "policy not found")
	}
	return nil
}

// ListRules returns the rules of a policy.
func (s *Store) ListRules(ctx context.Context, policyID string) ([]domain.Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.policy_id, p.name, r.kind, r.value, r.action, r.note, r.enabled, r.created_at, r.updated_at
		FROM rules r JOIN policies p ON p.id = r.policy_id
		WHERE r.policy_id = $1 ORDER BY r.kind, r.value`, policyID)
	if err != nil {
		return nil, apperr.Internal("list rules", err)
	}
	defer rows.Close()
	return scanRules(rows)
}

func scanRules(rows pgx.Rows) ([]domain.Rule, error) {
	var out []domain.Rule
	for rows.Next() {
		var r domain.Rule
		if err := rows.Scan(&r.ID, &r.PolicyID, &r.PolicyName, &r.Kind, &r.Value, &r.Action, &r.Note, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRule adds a rule to a policy.
func (s *Store) CreateRule(ctx context.Context, policyID string, input domain.RuleInput) (domain.Rule, error) {
	if _, err := s.GetPolicy(ctx, policyID); err != nil {
		return domain.Rule{}, err
	}
	ruleID := id.New()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rules (id, policy_id, kind, value, action, note, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ruleID, policyID, input.Kind, input.Value, input.Action, input.Note, input.Enabled)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Rule{}, apperr.Conflict("RULE_DUPLICATE", "this policy already contains an identical rule")
		}
		return domain.Rule{}, apperr.Internal("create rule", err)
	}
	return s.getRule(ctx, policyID, ruleID)
}

// CreateRules inserts several rules in one transaction, skipping duplicates.
func (s *Store) CreateRules(ctx context.Context, policyID string, inputs []domain.RuleInput) ([]domain.Rule, []string, error) {
	if len(inputs) == 0 {
		return nil, nil, nil
	}
	if _, err := s.GetPolicy(ctx, policyID); err != nil {
		return nil, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, apperr.Internal("begin rule batch", err)
	}
	defer tx.Rollback(ctx)

	ids := make([]string, 0, len(inputs))
	skipped := make([]string, 0)
	for _, input := range inputs {
		ruleID := id.New()
		tag, err := tx.Exec(ctx, `
			INSERT INTO rules (id, policy_id, kind, value, action, note, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (policy_id, kind, value) DO NOTHING`,
			ruleID, policyID, input.Kind, input.Value, input.Action, input.Note, input.Enabled)
		if err != nil {
			return nil, nil, apperr.Internal("create rules", err)
		}
		if tag.RowsAffected() == 0 {
			skipped = append(skipped, input.Value)
			continue
		}
		ids = append(ids, ruleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, apperr.Internal("commit rule batch", err)
	}

	rules := make([]domain.Rule, 0, len(ids))
	for _, ruleID := range ids {
		rule, err := s.getRule(ctx, policyID, ruleID)
		if err != nil {
			return nil, nil, err
		}
		rules = append(rules, rule)
	}
	return rules, skipped, nil
}

// UpdateRule updates a rule.
func (s *Store) UpdateRule(ctx context.Context, policyID, ruleID string, input domain.RuleInput) (domain.Rule, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE rules SET kind = $3, value = $4, action = $5, note = $6, enabled = $7, updated_at = now()
		WHERE id = $1 AND policy_id = $2`,
		ruleID, policyID, input.Kind, input.Value, input.Action, input.Note, input.Enabled)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Rule{}, apperr.Conflict("RULE_DUPLICATE", "this policy already contains an identical rule")
		}
		return domain.Rule{}, apperr.Internal("update rule", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Rule{}, apperr.NotFound("RULE_NOT_FOUND", "rule not found")
	}
	return s.getRule(ctx, policyID, ruleID)
}

func (s *Store) getRule(ctx context.Context, policyID, ruleID string) (domain.Rule, error) {
	var r domain.Rule
	err := s.pool.QueryRow(ctx, `
		SELECT r.id, r.policy_id, p.name, r.kind, r.value, r.action, r.note, r.enabled, r.created_at, r.updated_at
		FROM rules r JOIN policies p ON p.id = r.policy_id
		WHERE r.id = $1 AND r.policy_id = $2`, ruleID, policyID).
		Scan(&r.ID, &r.PolicyID, &r.PolicyName, &r.Kind, &r.Value, &r.Action, &r.Note, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Rule{}, apperr.NotFound("RULE_NOT_FOUND", "rule not found")
	}
	if err != nil {
		return domain.Rule{}, apperr.Internal("get rule", err)
	}
	return r, nil
}

// DeleteRule removes a rule.
func (s *Store) DeleteRule(ctx context.Context, policyID, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM rules WHERE id = $1 AND policy_id = $2`, ruleID, policyID)
	if err != nil {
		return apperr.Internal("delete rule", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("RULE_NOT_FOUND", "rule not found")
	}
	return nil
}

// NodeIDsAffectedByPolicy returns nodes that receive a policy.
func (s *Store) NodeIDsAffectedByPolicy(ctx context.Context, policyID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id FROM node_policies WHERE policy_id = $1`, policyID)
	if err != nil {
		return nil, apperr.Internal("list affected nodes", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, apperr.Internal("scan affected node", err)
		}
		ids = append(ids, nodeID)
	}
	return ids, rows.Err()
}

// LatestRevision returns the newest policy revision version.
func (s *Store) LatestRevision(ctx context.Context) (int64, error) {
	var version int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM policy_revisions`).Scan(&version)
	if err != nil {
		return 0, apperr.Internal("read policy revision", err)
	}
	return version, nil
}

// CreateRevision serializes a new global revision record. It is called inside
// the service layer right after a policy mutation.
func (s *Store) CreateRevision(ctx context.Context, actor string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, apperr.Internal("begin revision", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731201)`); err != nil {
		return 0, apperr.Internal("lock revision", err)
	}
	var version int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM policy_revisions`).Scan(&version); err != nil {
		return 0, apperr.Internal("next revision", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.policy_id, p.name, r.kind, r.value, r.action, r.note, r.enabled,
		       r.created_at, r.updated_at
		FROM rules r JOIN policies p ON p.id = r.policy_id ORDER BY r.policy_id, r.kind, r.value`)
	if err != nil {
		return 0, apperr.Internal("load revision rules", err)
	}
	rules, err := scanRules(rows)
	rows.Close()
	if err != nil {
		return 0, apperr.Internal("scan revision rules", err)
	}
	snapshot := map[string]any{"version": version, "rules": rules}
	if _, err := tx.Exec(ctx, `INSERT INTO policy_revisions (version, actor, snapshot) VALUES ($1, $2, $3)`,
		version, actor, snapshot); err != nil {
		return 0, apperr.Internal("insert revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, apperr.Internal("commit revision", err)
	}
	return version, nil
}

// SnapshotForNode builds the effective enabled rule set for a node inside a
// repeatable-read transaction so the version and rules stay consistent.
func (s *Store) SnapshotForNode(ctx context.Context, nodeID string) (domain.Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Snapshot{}, apperr.Internal("begin snapshot", err)
	}
	defer tx.Rollback(ctx)

	var version int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM policy_revisions`).Scan(&version); err != nil {
		return domain.Snapshot{}, apperr.Internal("snapshot revision", err)
	}

	prows, err := tx.Query(ctx, `
		SELECT p.id, p.name FROM policies p
		JOIN node_policies np ON np.policy_id = p.id
		WHERE np.node_id = $1 AND p.enabled ORDER BY p.name`, nodeID)
	if err != nil {
		return domain.Snapshot{}, apperr.Internal("snapshot policies", err)
	}
	policies := []domain.PolicyMeta{}
	for prows.Next() {
		var meta domain.PolicyMeta
		if err := prows.Scan(&meta.ID, &meta.Name); err != nil {
			prows.Close()
			return domain.Snapshot{}, apperr.Internal("scan snapshot policy", err)
		}
		policies = append(policies, meta)
	}
	prows.Close()

	rrows, err := tx.Query(ctx, `
		SELECT r.id, r.policy_id, p.name, r.kind, r.value, r.action, r.note, r.enabled,
		       r.created_at, r.updated_at
		FROM rules r
		JOIN policies p ON p.id = r.policy_id
		JOIN node_policies np ON np.policy_id = p.id
		WHERE np.node_id = $1 AND p.enabled AND r.enabled
		ORDER BY r.kind, r.value`, nodeID)
	if err != nil {
		return domain.Snapshot{}, apperr.Internal("snapshot rules", err)
	}
	rules, err := scanRules(rrows)
	rrows.Close()
	if err != nil {
		return domain.Snapshot{}, apperr.Internal("scan snapshot rules", err)
	}
	if rules == nil {
		rules = []domain.Rule{}
	}
	return domain.Snapshot{Version: version, NodeID: nodeID, Rules: rules, Policies: policies}, nil
}

// NodePolicyIDsForSnapshot is a small helper used by tests.
func (s *Store) policyExists(ctx context.Context, policyID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM policies WHERE id = $1)`, policyID).Scan(&exists); err != nil {
		return apperr.Internal("check policy", err)
	}
	if !exists {
		return apperr.NotFound("POLICY_NOT_FOUND", fmt.Sprintf("policy %s not found", policyID))
	}
	return nil
}
