package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// ReplaceASNCatalog atomically replaces the searchable ASN directory.
func (s *Store) ReplaceASNCatalog(ctx context.Context, entries []domain.ASNEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("refusing to replace the ASN catalog with an empty set")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return apperr.Internal("begin asn catalog", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `TRUNCATE asn_catalog`); err != nil {
		return apperr.Internal("truncate asn catalog", err)
	}
	rows := make([][]any, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []any{entry.ASN, entry.Name, entry.Country})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"asn_catalog"}, []string{"asn", "name", "country"}, pgx.CopyFromRows(rows)); err != nil {
		return apperr.Internal("copy asn catalog", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Internal("commit asn catalog", err)
	}
	return nil
}

// SearchASNCatalog filters by country and by name or ASN.
func (s *Store) SearchASNCatalog(ctx context.Context, country, query string, limit, offset int) ([]domain.ASNEntry, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	const filter = `($1 = '' OR c.country = $1)
		AND ($2 = '' OR c.name ILIKE '%' || $2 || '%'
			OR c.asn::text ILIKE '%' || $2 || '%'
			OR ('AS' || c.asn) ILIKE '%' || $2 || '%')`

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM asn_catalog c WHERE `+filter, country, query).Scan(&total); err != nil {
		return nil, 0, apperr.Internal("count asn catalog", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.asn, c.name, c.country, COALESCE(array_length(p.prefixes, 1), 0)
		FROM asn_catalog c
		LEFT JOIN asn_prefixes p ON p.asn = c.asn
		WHERE `+filter+`
		ORDER BY CASE
			WHEN c.asn::text = $2 OR ('AS' || c.asn) ILIKE $2 THEN 0
			ELSE 1
		END, c.asn
		LIMIT $3 OFFSET $4`, country, query, limit, offset)
	if err != nil {
		return nil, 0, apperr.Internal("search asn catalog", err)
	}
	defer rows.Close()
	out := make([]domain.ASNEntry, 0, limit)
	for rows.Next() {
		var entry domain.ASNEntry
		if err := rows.Scan(&entry.ASN, &entry.Name, &entry.Country, &entry.PrefixCount); err != nil {
			return nil, 0, apperr.Internal("scan asn catalog", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperr.Internal("read asn catalog", err)
	}
	return out, total, nil
}

// ASNCountries returns the country facet with member counts.
func (s *Store) ASNCountries(ctx context.Context) ([]domain.ASNCountry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT country, count(*) FROM asn_catalog
		WHERE country <> '' GROUP BY country ORDER BY country`)
	if err != nil {
		return nil, apperr.Internal("list asn countries", err)
	}
	defer rows.Close()
	out := make([]domain.ASNCountry, 0, 256)
	for rows.Next() {
		var country domain.ASNCountry
		if err := rows.Scan(&country.Code, &country.Count); err != nil {
			return nil, apperr.Internal("scan asn country", err)
		}
		out = append(out, country)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Internal("read asn countries", err)
	}
	return out, nil
}

// ASNCatalogStats returns the directory size and last replacement time.
func (s *Store) ASNCatalogStats(ctx context.Context) (domain.ASNCatalogStats, error) {
	var stats domain.ASNCatalogStats
	var updated *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT count(*), max(updated_at) FROM asn_catalog`).
		Scan(&stats.Count, &updated); err != nil {
		return domain.ASNCatalogStats{}, apperr.Internal("read asn catalog stats", err)
	}
	stats.UpdatedAt = updated
	return stats, nil
}
