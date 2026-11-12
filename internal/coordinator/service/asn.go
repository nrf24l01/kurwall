package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"

	asnresolver "github.com/kurwall/kurwall/internal/coordinator/asn"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

// ASNSettings controls cache freshness and background retries.
type ASNSettings struct {
	Enabled      bool
	TTL          time.Duration
	Retry        time.Duration
	Catalog      bool
	CatalogTTL   time.Duration
	CatalogRetry time.Duration
}

// ASN resolves autonomous system numbers to announced prefixes and caches the
// result in PostgreSQL so snapshots stay available when the resolver is down.
type ASN struct {
	repo     domain.Repository
	resolver *asnresolver.Resolver
	catalog  *asnresolver.Catalog
	notifier Notifier
	logger   zerolog.Logger
	settings ASNSettings
	group    singleflight.Group
}

// NewASN builds the ASN cache service.
func NewASN(repo domain.Repository, resolver *asnresolver.Resolver, catalog *asnresolver.Catalog, notifier Notifier, logger zerolog.Logger, settings ASNSettings) *ASN {
	return &ASN{repo: repo, resolver: resolver, catalog: catalog, notifier: notifier, logger: logger, settings: settings}
}

// Enabled reports whether outbound ASN resolution is configured.
func (s *ASN) Enabled() bool { return s.settings.Enabled && s.resolver != nil }

// Ensure returns a fresh cache entry, resolving the ASN when necessary.
func (s *ASN) Ensure(ctx context.Context, asn int64, force bool) (domain.ASNCache, error) {
	if !s.Enabled() {
		return domain.ASNCache{}, fmt.Errorf("ASN resolution is disabled")
	}
	cache, found, err := s.repo.GetASNCache(ctx, asn)
	if err != nil {
		return domain.ASNCache{}, err
	}
	now := time.Now().UTC()
	if !force && found && s.fresh(cache, now) {
		return cache, nil
	}

	key := strconv.FormatInt(asn, 10)
	value, err, _ := s.group.Do(key, func() (any, error) {
		// Re-read inside the singleflight in case another caller completed it.
		current, ok, readErr := s.repo.GetASNCache(ctx, asn)
		if readErr != nil {
			return domain.ASNCache{}, readErr
		}
		if !force && ok && s.fresh(current, time.Now().UTC()) {
			return current, nil
		}
		result, resolveErr := s.resolver.Lookup(ctx, asn)
		attempt := time.Now().UTC()
		if resolveErr != nil {
			next := current
			next.ASN = asn
			next.RefreshedAt = attempt
			next.Error = resolveErr.Error()
			if !ok {
				next.Prefixes = []string{}
			}
			if saveErr := s.repo.SaveASNCache(ctx, next); saveErr != nil {
				s.logger.Error().Err(saveErr).Int64("asn", asn).Msg("failed to store ASN failure")
			}
			return next, resolveErr
		}
		next := domain.ASNCache{
			ASN: asn, Holder: result.Holder, Prefixes: result.Prefixes,
			UpdatedAt: attempt, RefreshedAt: attempt,
		}
		if saveErr := s.repo.SaveASNCache(ctx, next); saveErr != nil {
			return domain.ASNCache{}, saveErr
		}
		return next, nil
	})
	if err != nil {
		if cache.ASN == 0 {
			cache.ASN = asn
		}
		if value != nil {
			if resolved, ok := value.(domain.ASNCache); ok {
				cache = resolved
			}
		}
		return cache, err
	}
	resolved, _ := value.(domain.ASNCache)
	return resolved, nil
}

func (s *ASN) fresh(cache domain.ASNCache, now time.Time) bool {
	if cache.Error != "" {
		return false
	}
	if cache.UpdatedAt.IsZero() {
		return false
	}
	return now.Sub(cache.UpdatedAt) < s.settings.TTL
}

func (s *ASN) due(cache domain.ASNCache, found bool, now time.Time) bool {
	if !found {
		return true
	}
	if cache.Error != "" {
		return now.Sub(cache.RefreshedAt) >= s.settings.Retry
	}
	return cache.UpdatedAt.IsZero() || now.Sub(cache.UpdatedAt) >= s.settings.TTL
}

// SearchCatalog filters the local ASN directory by country and name/ASN.
func (s *ASN) SearchCatalog(ctx context.Context, country, query string, limit, offset int) ([]domain.ASNEntry, int64, error) {
	return s.repo.SearchASNCatalog(ctx, strings.ToUpper(strings.TrimSpace(country)), strings.TrimSpace(query), limit, offset)
}

// Countries returns country facets for the ASN directory.
func (s *ASN) Countries(ctx context.Context) ([]domain.ASNCountry, error) {
	return s.repo.ASNCountries(ctx)
}

// CatalogStats returns directory size and freshness.
func (s *ASN) CatalogStats(ctx context.Context) (domain.ASNCatalogStats, error) {
	return s.repo.ASNCatalogStats(ctx)
}

// RunCatalog refreshes the searchable ASN directory when enabled.
func (s *ASN) RunCatalog(ctx context.Context) {
	if s.catalog == nil || !s.settings.Catalog {
		return
	}
	s.refreshCatalog(ctx)
	ticker := time.NewTicker(s.settings.CatalogRetry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshCatalog(ctx)
		}
	}
}

func (s *ASN) refreshCatalog(ctx context.Context) {
	stats, err := s.repo.ASNCatalogStats(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to read ASN catalog stats")
		return
	}
	rangeCount, err := s.repo.CountASNRanges(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to count ASN ranges")
		return
	}
	if stats.Count > 0 && stats.UpdatedAt != nil && rangeCount > 0 && time.Since(*stats.UpdatedAt) < s.settings.CatalogTTL {
		return
	}
	result, err := s.catalog.Load(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("ASN catalog refresh failed")
		return
	}
	entries := make([]domain.ASNEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, domain.ASNEntry{ASN: entry.ASN, Name: entry.Name, Country: entry.Country})
	}
	ranges := make([]domain.ASNRange, 0, len(result.Ranges))
	for _, r := range result.Ranges {
		ranges = append(ranges, domain.ASNRange{
			StartIP: r.Start, EndIP: r.End,
			ASN: r.ASN, Country: r.Country, Name: r.Name,
		})
	}
	if err := s.repo.ReplaceASNCatalog(ctx, entries, ranges); err != nil {
		s.logger.Error().Err(err).Msg("failed to replace ASN catalog")
		return
	}
	s.logger.Info().Int("asns", len(entries)).Int("ranges", len(ranges)).Msg("ASN catalog updated")
}

// Cached returns the current cache entry without resolving.
func (s *ASN) Cached(ctx context.Context, asn int64) (domain.ASNCache, bool, error) {
	return s.repo.GetASNCache(ctx, asn)
}

// Run refreshes due ASN entries until ctx is cancelled. The ticker is the
// retry interval; fresh entries are skipped without a network call.
func (s *ASN) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.refresh(ctx)
	ticker := time.NewTicker(s.settings.Retry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *ASN) refresh(ctx context.Context) {
	asns, err := s.repo.ASNsInUse(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list ASN rules for refresh")
		return
	}
	now := time.Now().UTC()
	changed := false
	for _, asn := range asns {
		if ctx.Err() != nil {
			return
		}
		old, found, err := s.repo.GetASNCache(ctx, asn)
		if err != nil {
			s.logger.Warn().Err(err).Int64("asn", asn).Msg("failed to read ASN cache")
			continue
		}
		if !s.due(old, found, now) {
			continue
		}
		next, resolveErr := s.Ensure(ctx, asn, true)
		if resolveErr != nil {
			s.logger.Warn().Err(resolveErr).Int64("asn", asn).Msg("ASN refresh failed")
		}
		if found && (next.Holder != old.Holder || !samePrefixes(next.Prefixes, old.Prefixes)) {
			changed = true
			s.logger.Info().Int64("asn", asn).Int("prefixes", len(next.Prefixes)).Msg("ASN prefixes changed")
		}
		if !found && len(next.Prefixes) > 0 {
			changed = true
		}
		// Be polite to the public BGP source.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !changed {
		return
	}
	if _, err := s.repo.CreateRevision(ctx, "asn-refresh"); err != nil {
		s.logger.Error().Err(err).Msg("failed to publish ASN revision")
		return
	}
	nodeIDs, err := s.repo.ActiveNodeIDs(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to list nodes for ASN push")
		return
	}
	if s.notifier != nil {
		s.notifier.PushPolicy(ctx, nodeIDs)
	}
	s.logger.Info().Int("nodes", len(nodeIDs)).Msg("ASN prefix revision published")
}

func samePrefixes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
