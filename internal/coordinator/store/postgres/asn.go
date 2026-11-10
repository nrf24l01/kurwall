package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

// GetASNCache returns the last resolved prefix set for an ASN.
func (s *Store) GetASNCache(ctx context.Context, asn int64) (domain.ASNCache, bool, error) {
	var cache domain.ASNCache
	var updated *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT asn, holder, prefixes, refreshed_at, updated_at, error
		FROM asn_prefixes WHERE asn = $1`, asn).
		Scan(&cache.ASN, &cache.Holder, &cache.Prefixes, &cache.RefreshedAt, &updated, &cache.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ASNCache{}, false, nil
	}
	if err != nil {
		return domain.ASNCache{}, false, apperr.Internal("read asn cache", err)
	}
	if updated != nil {
		cache.UpdatedAt = *updated
	}
	if cache.Prefixes == nil {
		cache.Prefixes = []string{}
	}
	return cache, true, nil
}

// SaveASNCache stores a successful or failed resolution attempt. On failure the
// caller keeps the previous prefixes and updates only the attempt timestamp.
func (s *Store) SaveASNCache(ctx context.Context, cache domain.ASNCache) error {
	var updated *time.Time
	if !cache.UpdatedAt.IsZero() {
		updated = &cache.UpdatedAt
	}
	refreshed := cache.RefreshedAt
	if refreshed.IsZero() {
		refreshed = time.Now().UTC()
	}
	if cache.Prefixes == nil {
		cache.Prefixes = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO asn_prefixes (asn, holder, prefixes, refreshed_at, updated_at, error)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (asn) DO UPDATE SET
			holder = EXCLUDED.holder,
			prefixes = EXCLUDED.prefixes,
			refreshed_at = EXCLUDED.refreshed_at,
			updated_at = EXCLUDED.updated_at,
			error = EXCLUDED.error`,
		cache.ASN, cache.Holder, cache.Prefixes, refreshed, updated, cache.Error)
	if err != nil {
		return apperr.Internal("save asn cache", err)
	}
	return nil
}

// ASNsInUse returns the enabled ASN rules' numbers.
func (s *Store) ASNsInUse(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT value FROM rules WHERE kind = 'asn' AND enabled`)
	if err != nil {
		return nil, apperr.Internal("list asn rules", err)
	}
	defer rows.Close()
	seen := map[int64]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, apperr.Internal("scan asn rule", err)
		}
		asn, err := ruleutil.ParseASN(value)
		if err != nil {
			continue
		}
		seen[asn] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Internal("read asn rules", err)
	}
	out := make([]int64, 0, len(seen))
	for asn := range seen {
		out = append(out, asn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
