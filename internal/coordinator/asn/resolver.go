// Package asn resolves autonomous system numbers to their currently announced
// CIDR prefixes. It uses RIPEstat by default; the URL templates are
// configurable so a self-hosted BGP source can be used instead.
package asn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

const maxResponseBytes = 32 << 20

// Result is one resolved ASN.
type Result struct {
	ASN      int64
	Holder   string
	Prefixes []string
}

// Resolver calls the configured BGP data source.
type Resolver struct {
	client      *http.Client
	prefixURL   string
	holderURL   string
	maxPrefixes int
	logger      zerolog.Logger
}

// New builds a resolver. Both URL templates must contain {asn}. The holder URL
// may be empty to skip the AS holder lookup.
func New(prefixURL, holderURL string, timeout time.Duration, maxPrefixes int, logger zerolog.Logger) *Resolver {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxPrefixes <= 0 {
		maxPrefixes = 20000
	}
	return &Resolver{
		client:      &http.Client{Timeout: timeout},
		prefixURL:   prefixURL,
		holderURL:   holderURL,
		maxPrefixes: maxPrefixes,
		logger:      logger,
	}
}

// Lookup fetches the announced prefixes and, when configured, the holder name.
func (r *Resolver) Lookup(ctx context.Context, asn int64) (Result, error) {
	if asn <= 0 {
		return Result{}, fmt.Errorf("invalid ASN")
	}
	result := Result{ASN: asn}
	prefixes, err := r.fetchPrefixes(ctx, asn)
	if err != nil {
		return Result{}, err
	}
	result.Prefixes = prefixes
	if r.holderURL != "" {
		if holder, err := r.fetchHolder(ctx, asn); err == nil {
			result.Holder = holder
		} else {
			r.logger.Debug().Err(err).Int64("asn", asn).Msg("asn holder lookup failed")
		}
	}
	return result, nil
}

func (r *Resolver) fetchPrefixes(ctx context.Context, asn int64) ([]string, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Prefixes []struct {
				Prefix string `json:"prefix"`
			} `json:"prefixes"`
		} `json:"data"`
	}
	if err := r.getJSON(ctx, r.prefixURL, asn, &payload); err != nil {
		return nil, err
	}
	if payload.Status != "" && payload.Status != "ok" {
		return nil, fmt.Errorf("prefix source returned status %q", payload.Status)
	}
	seen := map[string]struct{}{}
	parsed := make([]netip.Prefix, 0, len(payload.Data.Prefixes))
	for _, item := range payload.Data.Prefixes {
		prefix, err := ruleutil.NormalizeCIDR(item.Prefix)
		if err != nil {
			continue
		}
		key := prefix.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parsed = append(parsed, prefix)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("AS%d has no announced prefixes", asn)
	}
	if len(parsed) > r.maxPrefixes {
		return nil, fmt.Errorf("AS%d resolves to %d prefixes, above the %d limit", asn, len(parsed), r.maxPrefixes)
	}
	sort.Slice(parsed, func(i, j int) bool {
		if parsed[i].Addr().Is4() != parsed[j].Addr().Is4() {
			return parsed[i].Addr().Is4()
		}
		if cmp := parsed[i].Addr().Compare(parsed[j].Addr()); cmp != 0 {
			return cmp < 0
		}
		return parsed[i].Bits() < parsed[j].Bits()
	})
	out := make([]string, 0, len(parsed))
	for _, prefix := range parsed {
		out = append(out, prefix.String())
	}
	return out, nil
}

func (r *Resolver) fetchHolder(ctx context.Context, asn int64) (string, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Holder string `json:"holder"`
		} `json:"data"`
	}
	if err := r.getJSON(ctx, r.holderURL, asn, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.Data.Holder), nil
}

func (r *Resolver) getJSON(ctx context.Context, template string, asn int64, dst any) error {
	url := strings.ReplaceAll(template, "{asn}", strconv.FormatInt(asn, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build asn request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Kurwall/0.1 ASN resolver")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("asn source request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("asn source returned HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode asn source response: %w", err)
	}
	return nil
}

// ValidateTemplates verifies the configured URL templates before startup.
func ValidateTemplates(prefixURL, holderURL string) error {
	if !strings.Contains(prefixURL, "{asn}") {
		return fmt.Errorf("asn prefix URL template must contain {asn}")
	}
	if holderURL != "" && !strings.Contains(holderURL, "{asn}") {
		return fmt.Errorf("asn holder URL template must contain {asn}")
	}
	return nil
}
