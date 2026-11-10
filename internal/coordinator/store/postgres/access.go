package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/id"
)

// CountUsers returns the total number of accounts, including disabled ones.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return 0, apperr.Internal("count users", err)
	}
	return count, nil
}

// CreateUser inserts an account.
func (s *Store) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	user.ID = id.New()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, username, password_hash, role, disabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at`,
		user.ID, user.Username, user.PasswordHash, user.Role, user.Disabled).
		Scan(&user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.User{}, apperr.Conflict("USERNAME_TAKEN", "this username already exists")
		}
		return domain.User{}, apperr.Internal("create user", err)
	}
	return user, nil
}

// GetUserByUsername returns an account by normalized username.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (domain.User, bool, error) {
	return s.getUser(ctx, `SELECT id, username, password_hash, role, disabled, created_at, updated_at FROM users WHERE username = $1`, username)
}

// GetUserByID returns an account by id.
func (s *Store) GetUserByID(ctx context.Context, userID string) (domain.User, bool, error) {
	return s.getUser(ctx, `SELECT id, username, password_hash, role, disabled, created_at, updated_at FROM users WHERE id = $1`, userID)
}

func (s *Store) getUser(ctx context.Context, query string, arg string) (domain.User, bool, error) {
	var user domain.User
	err := s.pool.QueryRow(ctx, query, arg).Scan(
		&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, apperr.Internal("get user", err)
	}
	return user, true, nil
}

// ListUsers returns all accounts ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, password_hash, role, disabled, created_at, updated_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, apperr.Internal("list users", err)
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, apperr.Internal("scan user", err)
		}
		out = append(out, user)
	}
	return out, rows.Err()
}

// UpdateUserPassword replaces a password hash and revokes existing sessions.
func (s *Store) UpdateUserPassword(ctx context.Context, username, passwordHash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE username = $1`, username, passwordHash)
	if err != nil {
		return false, apperr.Internal("update user password", err)
	}
	if tag.RowsAffected() > 0 {
		if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = $1)`, username); err != nil {
			return false, apperr.Internal("revoke user sessions", err)
		}
	}
	return tag.RowsAffected() > 0, nil
}

// SetUserDisabled enables or disables an account and revokes its sessions.
func (s *Store) SetUserDisabled(ctx context.Context, username string, disabled bool) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET disabled = $2, updated_at = now() WHERE username = $1`, username, disabled)
	if err != nil {
		return false, apperr.Internal("update user status", err)
	}
	if tag.RowsAffected() > 0 && disabled {
		_, _ = s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = $1)`, username)
	}
	return tag.RowsAffected() > 0, nil
}

// CreateSession stores a new login session.
func (s *Store) CreateSession(ctx context.Context, session domain.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, csrf_hash, expires_at, user_agent, remote_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		session.ID, session.UserID, session.TokenHash, session.CSRFHash, session.ExpiresAt, session.UserAgent, session.RemoteIP)
	if err != nil {
		return apperr.Internal("create session", err)
	}
	return nil
}

// GetSessionByTokenHash returns a session and its user.
func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, domain.User, bool, error) {
	var session domain.Session
	var user domain.User
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.token_hash, s.csrf_hash, s.created_at, s.expires_at, s.last_seen_at,
		       s.user_agent, s.remote_ip,
		       u.id, u.username, u.password_hash, u.role, u.disabled, u.created_at, u.updated_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, tokenHash).Scan(
		&session.ID, &session.UserID, &session.TokenHash, &session.CSRFHash, &session.CreatedAt, &session.ExpiresAt,
		&session.LastSeenAt, &session.UserAgent, &session.RemoteIP,
		&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.User{}, false, nil
	}
	if err != nil {
		return domain.Session{}, domain.User{}, false, apperr.Internal("get session", err)
	}
	return session, user, true, nil
}

// TouchSession records recent activity.
func (s *Store) TouchSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1 AND expires_at > $2`, sessionID, now)
	return err
}

// DeleteSessionByTokenHash removes one session.
func (s *Store) DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return apperr.Internal("delete session", err)
	}
	return nil
}

// DeleteExpiredSessions removes stale sessions and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, before)
	if err != nil {
		return 0, apperr.Internal("delete expired sessions", err)
	}
	return tag.RowsAffected(), nil
}

// CreateDeployKey stores a pending node enrollment.
func (s *Store) CreateDeployKey(ctx context.Context, key domain.DeployKey) (domain.DeployKey, error) {
	key.ID = id.New()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO deploy_keys (id, node_name, token_hash, policy_ids, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		key.ID, key.NodeName, key.TokenHash, key.PolicyIDs, key.CreatedBy, key.ExpiresAt).
		Scan(&key.CreatedAt)
	if err != nil {
		return domain.DeployKey{}, apperr.Internal("create deploy key", err)
	}
	if key.PolicyIDs == nil {
		key.PolicyIDs = []string{}
	}
	return key, nil
}

// ListDeployKeys returns recent deploy keys without plaintext tokens.
func (s *Store) ListDeployKeys(ctx context.Context) ([]domain.DeployKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, node_name, token_hash, policy_ids, created_by, created_at, expires_at, used_at, used_by_ip
		FROM deploy_keys ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, apperr.Internal("list deploy keys", err)
	}
	defer rows.Close()
	var out []domain.DeployKey
	for rows.Next() {
		var key domain.DeployKey
		if err := rows.Scan(&key.ID, &key.NodeName, &key.TokenHash, &key.PolicyIDs, &key.CreatedBy,
			&key.CreatedAt, &key.ExpiresAt, &key.UsedAt, &key.UsedByIP); err != nil {
			return nil, apperr.Internal("scan deploy key", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// DeleteDeployKey revokes an unused deploy key.
func (s *Store) DeleteDeployKey(ctx context.Context, keyID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM deploy_keys WHERE id = $1 AND used_at IS NULL`, keyID)
	if err != nil {
		return false, apperr.Internal("delete deploy key", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ConsumeDeployKey atomically validates and marks a key as used.
func (s *Store) ConsumeDeployKey(ctx context.Context, tokenHash []byte, remoteIP string, now time.Time) (domain.DeployKey, bool, error) {
	var key domain.DeployKey
	err := s.pool.QueryRow(ctx, `
		UPDATE deploy_keys
		SET used_at = $2, used_by_ip = $3
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING id, node_name, token_hash, policy_ids, created_by, created_at, expires_at, used_at, used_by_ip`,
		tokenHash, now, remoteIP).Scan(
		&key.ID, &key.NodeName, &key.TokenHash, &key.PolicyIDs, &key.CreatedBy,
		&key.CreatedAt, &key.ExpiresAt, &key.UsedAt, &key.UsedByIP)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeployKey{}, false, nil
	}
	if err != nil {
		return domain.DeployKey{}, false, apperr.Internal("consume deploy key", err)
	}
	return key, true, nil
}

// NodeExists reports whether a node id is already registered.
func (s *Store) NodeExists(ctx context.Context, nodeID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id = $1)`, nodeID).Scan(&exists); err != nil {
		return false, apperr.Internal("check node", err)
	}
	return exists, nil
}

// AssignEnrolledNode creates a pending node and assigns policies, returning
// the number of new assignments.
func (s *Store) AssignEnrolledNode(ctx context.Context, nodeID string, policyIDs []string) (int64, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO nodes (id, name, status) VALUES ($1, $1, 'pending')
		ON CONFLICT (id) DO NOTHING`, nodeID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, apperr.Conflict("NODE_NAME_TAKEN", "a node with this name already exists")
		}
		return 0, apperr.Internal("create enrolled node", err)
	}
	if len(policyIDs) == 0 {
		return 0, nil
	}
	assignments, err := s.pool.Exec(ctx, `
		INSERT INTO node_policies (node_id, policy_id)
		SELECT $1, id FROM policies WHERE id = ANY($2::text[])
		ON CONFLICT DO NOTHING`, nodeID, policyIDs)
	if err != nil {
		return 0, apperr.Internal("assign enrolled policies", err)
	}
	return assignments.RowsAffected(), nil
}
