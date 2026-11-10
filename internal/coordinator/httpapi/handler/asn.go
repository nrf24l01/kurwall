package handler

import (
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/ruleutil"
)

type asnResponse struct {
	ASN         string     `json:"asn"`
	Number      int64      `json:"number"`
	Holder      string     `json:"holder"`
	PrefixCount int        `json:"prefixCount"`
	Prefixes    []string   `json:"prefixes"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
	RefreshedAt time.Time  `json:"refreshedAt"`
	Error       string     `json:"error,omitempty"`
}

// LookupASN resolves an autonomous system for the rule editor. Pass
// ?refresh=1 to bypass a fresh cache entry.
func (h *Handler) LookupASN(c echo.Context) error {
	asn, err := ruleutil.ParseASN(c.Param("asn"))
	if err != nil {
		return response.Error(c, apperr.Validation(err.Error()))
	}
	if h.ASN == nil || !h.ASN.Enabled() {
		return response.Error(c, apperr.Validation("ASN resolution is disabled on this coordinator"))
	}
	cache, resolveErr := h.ASN.Ensure(c.Request().Context(), asn, c.QueryParam("refresh") == "1")
	out := asnResponse{
		ASN: fmt.Sprintf("AS%d", asn), Number: asn, Holder: cache.Holder,
		PrefixCount: len(cache.Prefixes), Prefixes: cache.Prefixes,
		RefreshedAt: cache.RefreshedAt,
	}
	if !cache.UpdatedAt.IsZero() {
		out.UpdatedAt = &cache.UpdatedAt
	}
	if resolveErr != nil {
		out.Error = resolveErr.Error()
		if len(cache.Prefixes) == 0 {
			return response.Error(c, apperr.Unavailable("ASN_RESOLUTION_FAILED", "the autonomous system could not be resolved", resolveErr))
		}
	}
	return response.OK(c, out)
}

type asnPage struct {
	Items            []domain.ASNEntry `json:"items"`
	Total            int64             `json:"total"`
	Limit            int               `json:"limit"`
	Offset           int               `json:"offset"`
	CatalogUpdatedAt *time.Time        `json:"catalogUpdatedAt,omitempty"`
}

// ListASNs searches the local autonomous system directory.
func (h *Handler) ListASNs(c echo.Context) error {
	country := strings.ToUpper(strings.TrimSpace(c.QueryParam("country")))
	if country != "" && (len(country) != 2 || !isCountryCode(country)) {
		return response.Error(c, apperr.Validation("country must be a two-letter code"))
	}
	query := strings.TrimSpace(c.QueryParam("q"))
	if len(query) > 100 {
		return response.Error(c, apperr.Validation("q must be at most 100 characters"))
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		return response.Error(c, err)
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return response.Error(c, err)
	}
	if h.ASN == nil {
		return response.Error(c, apperr.Unavailable("ASN_CATALOG_UNAVAILABLE", "ASN directory is unavailable", nil))
	}
	items, total, err := h.ASN.SearchCatalog(c.Request().Context(), country, query, limit, offset)
	if err != nil {
		return response.Error(c, err)
	}
	stats, _ := h.ASN.CatalogStats(c.Request().Context())
	pageLimit := limit
	if pageLimit <= 0 {
		pageLimit = 50
	}
	if pageLimit > 500 {
		pageLimit = 500
	}
	return response.OK(c, asnPage{
		Items: items, Total: total, Limit: pageLimit, Offset: max(offset, 0), CatalogUpdatedAt: stats.UpdatedAt,
	})
}

// ListASNCountries returns country facets for the ASN directory.
func (h *Handler) ListASNCountries(c echo.Context) error {
	if h.ASN == nil {
		return response.OK(c, map[string]any{"items": []domain.ASNCountry{}})
	}
	countries, err := h.ASN.Countries(c.Request().Context())
	if err != nil {
		return response.Error(c, err)
	}
	return response.OK(c, map[string]any{"items": countries})
}

func isCountryCode(value string) bool {
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
