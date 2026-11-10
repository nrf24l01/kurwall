package handler

import (
	"context"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// Ping is the cheap liveness probe used by the container healthcheck.
func (h *Handler) Ping(c echo.Context) error {
	return response.OK(c, map[string]any{"status": "ok"})
}

// Health reports mandatory dependency availability.
func (h *Handler) Health(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 4*time.Second)
	defer cancel()
	if h.DBHealth != nil {
		if err := h.DBHealth(ctx); err != nil {
			return response.Error(c, apperr.Unavailable("POSTGRES_UNAVAILABLE", "postgres is unavailable", err))
		}
	}
	if h.Events != nil {
		if err := h.Events.Health(ctx); err != nil {
			return response.Error(c, apperr.Unavailable("CLICKHOUSE_UNAVAILABLE", "clickhouse is unavailable", err))
		}
	}
	return response.OK(c, map[string]any{
		"status": "ok", "postgres": "ok", "clickhouse": "ok", "version": h.Version,
	})
}

// Meta exposes build and agent configuration information to the dashboard.
func (h *Handler) Meta(c echo.Context) error {
	base, _ := h.deployBaseURL(c)
	grpcAddr, _ := h.deployGRPCAddr(c)
	agentVersion, agentAvailable := h.agentDistribution()
	mode := "open"
	if h.Auth != nil {
		if current, err := h.Auth.Mode(c.Request().Context()); err == nil {
			mode = current
		} else {
			mode = "unavailable"
		}
	}
	return response.OK(c, map[string]any{
		"version":                 h.Version,
		"agentSettings":           h.Settings,
		"clickhouseRetentionDays": 30,
		"authRequired":            mode != "open",
		"authMode":                mode,
		"enrollmentAvailable":     h.Deploy != nil && h.Deploy.Enabled(),
		"publicUrl":               base,
		"grpcPublicAddr":          grpcAddr,
		"agentInstallUrl":         base + "/install.sh",
		"agentManifestUrl":        base + "/agent/manifest.json",
		"agentVersion":            agentVersion,
		"agentDistAvailable":      agentAvailable,
	})
}
