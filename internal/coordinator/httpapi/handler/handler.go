// Package handler implements the coordinator REST API.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/live"
	"github.com/kurwall/kurwall/internal/coordinator/service"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/logging"
)

// Handler groups the REST resources.
type Handler struct {
	Nodes          *service.Nodes
	Policies       *service.Policies
	Events         *service.Events
	ASN            *service.ASN
	Live           *live.Hub
	Auth           *service.Auth
	Deploy         *service.Deploy
	Version        string
	Settings       domain.AgentSettings
	DBHealth       func(ctx context.Context) error
	PublicURL      string
	GRPCPublicAddr string
	GRPCServerName string
	GRPCAddr       string
	AgentDistDir   string
	AgentCAFile    string
}

// New builds a handler set.
func New(nodes *service.Nodes, policies *service.Policies, events *service.Events, asn *service.ASN, liveHub *live.Hub, version string, settings domain.AgentSettings) *Handler {
	return &Handler{Nodes: nodes, Policies: policies, Events: events, ASN: asn, Live: liveHub, Version: version, Settings: settings}
}

func decodeJSON(c echo.Context, dst any) error {
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.InvalidRequest("request body must be valid JSON with known fields")
	}
	return nil
}

func actor(c echo.Context) string {
	if traceID := logging.TraceID(c.Request().Context()); traceID != "" {
		return "api:" + traceID
	}
	return "api"
}

func queryInt(c echo.Context, name string, def int) (int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.Validation(fmt.Sprintf("%s must be an integer", name))
	}
	return value, nil
}

func queryUint8(c echo.Context, name string) (*uint8, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return nil, apperr.Validation(fmt.Sprintf("%s must be an integer between 0 and 255", name))
	}
	v := uint8(value)
	return &v, nil
}

func queryBool(c echo.Context, name string) (*bool, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, apperr.Validation(fmt.Sprintf("%s must be a boolean", name))
	}
	return &value, nil
}

func queryTime(c echo.Context, name string) (time.Time, error) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, apperr.Validation(fmt.Sprintf("%s must be an RFC3339 timestamp", name))
}

func notFound(c echo.Context) error {
	return echo.NewHTTPError(http.StatusNotFound, "route not found")
}
