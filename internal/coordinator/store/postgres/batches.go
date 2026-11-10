package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// ClaimBatch implements idempotent event batch ingestion. A crashed
// coordinator's stale claim can be taken over after takeoverAfter.
func (s *Store) ClaimBatch(ctx context.Context, nodeID, batchID string, now time.Time, takeoverAfter time.Duration) (domain.Claim, error) {
	var committedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ingested_batches (node_id, batch_id, claimed_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (node_id, batch_id) DO NOTHING
		RETURNING committed_at`, nodeID, batchID, now).Scan(&committedAt)
	if err == nil {
		return domain.Claim{Claimed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Claim{}, apperr.Internal("claim event batch", err)
	}

	var claimedAt time.Time
	err = s.pool.QueryRow(ctx, `SELECT claimed_at, committed_at FROM ingested_batches WHERE node_id = $1 AND batch_id = $2`,
		nodeID, batchID).Scan(&claimedAt, &committedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The claim disappeared between statements (cleanup). Retry next time.
		return domain.Claim{}, nil
	}
	if err != nil {
		return domain.Claim{}, apperr.Internal("read event batch claim", err)
	}
	if committedAt != nil {
		return domain.Claim{Duplicate: true}, nil
	}

	cutoff := now.Add(-takeoverAfter)
	tag, err := s.pool.Exec(ctx, `
		UPDATE ingested_batches SET claimed_at = $3
		WHERE node_id = $1 AND batch_id = $2 AND committed_at IS NULL AND claimed_at < $4`,
		nodeID, batchID, now, cutoff)
	if err != nil {
		return domain.Claim{}, apperr.Internal("take over event batch claim", err)
	}
	if tag.RowsAffected() == 1 {
		return domain.Claim{Claimed: true}, nil
	}
	// Another worker owns a fresh claim.
	return domain.Claim{}, nil
}

// CommitBatch marks a batch as persisted so retries are acknowledged as
// duplicates without reinserting.
func (s *Store) CommitBatch(ctx context.Context, nodeID, batchID string, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE ingested_batches SET committed_at = $3 WHERE node_id = $1 AND batch_id = $2`,
		nodeID, batchID, now)
	if err != nil {
		return apperr.Internal("commit event batch", err)
	}
	return nil
}

// ReleaseBatch removes an uncommitted claim so the agent can retry later.
func (s *Store) ReleaseBatch(ctx context.Context, nodeID, batchID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ingested_batches WHERE node_id = $1 AND batch_id = $2 AND committed_at IS NULL`,
		nodeID, batchID)
	if err != nil {
		return apperr.Internal("release event batch", err)
	}
	return nil
}

// CleanupBatches removes old idempotency records.
func (s *Store) CleanupBatches(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ingested_batches WHERE committed_at IS NOT NULL AND committed_at < $1`, olderThan)
	if err != nil {
		return 0, apperr.Internal("cleanup event batches", err)
	}
	return tag.RowsAffected(), nil
}
