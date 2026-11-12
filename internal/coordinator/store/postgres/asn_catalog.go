package postgres

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// ReplaceASNCatalog atomically replaces the searchable ASN directory and the
// address ranges used for destination lookup.
func (s *Store) ReplaceASNCatalog(ctx context.Context, entries []domain.ASNEntry, ranges []domain.ASNRange) error {
	if len(entries) == 0 {
		return fmt.Errorf("refusing to replace the ASN catalog with an empty set")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return apperr.Internal("begin asn catalog", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `TRUNCATE asn_catalog, asn_ranges`); err != nil {
		return apperr.Internal("truncate asn catalog", err)
	}
	rows := make([][]any, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []any{entry.ASN, entry.Name, entry.Country})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"asn_catalog"}, []string{"asn", "name", "country"}, pgx.CopyFromRows(rows)); err != nil {
		return apperr.Internal("copy asn catalog", err)
	}
	if len(ranges) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"asn_ranges"}, []string{"start_ip", "end_ip", "asn", "country", "name"}, &asnRangeSource{ranges: ranges}); err != nil {
			return apperr.Internal("copy asn ranges", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Internal("commit asn catalog", err)
	}
	return nil
}

// asnRangeSource streams catalog ranges into pgx's binary COPY, avoiding a
// second full [][]any copy of the range set.
type asnRangeSource struct {
	ranges []domain.ASNRange
	index  int
	err    error
}

func (s *asnRangeSource) Next() bool {
	if s.err != nil || s.index >= len(s.ranges) {
		return false
	}
	s.index++
	return true
}

func (s *asnRangeSource) Values() ([]any, error) {
	r := s.ranges[s.index-1]
	if !r.StartIP.IsValid() || !r.EndIP.IsValid() {
		s.err = fmt.Errorf("invalid ASN range addresses for AS%d", r.ASN)
		return nil, s.err
	}
	return []any{r.StartIP, r.EndIP, r.ASN, r.Country, r.Name}, nil
}

func (s *asnRangeSource) Err() error { return s.err }

// CountASNRanges returns the number of rows in the address range table.
func (s *Store) CountASNRanges(ctx context.Context) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM asn_ranges`).Scan(&count); err != nil {
		return 0, apperr.Internal("count asn ranges", err)
	}
	return count, nil
}

// LookupASNForIPs maps destination addresses to their ASN and country. Inputs
// are normalized to unmapped form and unmatched addresses are omitted.
func (s *Store) LookupASNForIPs(ctx context.Context, ips []string) (map[string]domain.ASNInfo, error) {
	out := make(map[string]domain.ASNInfo, len(ips))
	if len(ips) == 0 {
		return out, nil
	}
	addrs := make([]netip.Addr, 0, len(ips))
	seen := make(map[netip.Addr]struct{}, len(ips))
	for _, raw := range ips {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT host(ip), COALESCE(r.asn, 0), COALESCE(r.country, ''), COALESCE(r.name, '')
		FROM unnest($1::inet[]) AS ip
		LEFT JOIN LATERAL (
			SELECT asn, country, name, end_ip
			FROM asn_ranges
			WHERE start_ip <= ip
			ORDER BY start_ip DESC
			LIMIT 1
		) r ON r.end_ip >= ip`, addrs)
	if err != nil {
		return nil, apperr.Internal("lookup ASN for IPs", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		var info domain.ASNInfo
		if err := rows.Scan(&ip, &info.ASN, &info.Country, &info.Name); err != nil {
			return nil, apperr.Internal("scan ASN lookup", err)
		}
		if info.ASN == 0 {
			continue
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return nil, apperr.Internal("normalize ASN lookup address", err)
		}
		out[addr.Unmap().String()] = info
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Internal("read ASN lookup", err)
	}
	return out, nil
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
