// Package middleware contains the coordinator HTTP middleware chain.
package middleware

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/platform/id"
	"github.com/kurwall/kurwall/internal/platform/logging"
)

// Authenticator resolves API tokens and browser sessions.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer, session string) (auth.Identity, error)
	CheckCSRF(identity auth.Identity, token string) bool
}

// Recover converts panics into the standard 500 envelope.
func Recover() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					logging.From(c.Request().Context()).Error().Interface("panic", r).
						Bytes("stack", debug.Stack()).Msg("http handler panicked")
					err = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
				}
			}()
			return next(c)
		}
	}
}

// Logger attaches the base logger to every request context so downstream
// layers can emit structured logs with the trace ID.
func Logger(base zerolog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := logging.WithLogger(c.Request().Context(), base)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// Trace assigns a validated trace ID to every request.
func Trace() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			traceID := strings.TrimSpace(c.Request().Header.Get("X-Trace-ID"))
			if !ValidTraceID(traceID) {
				traceID = id.New()
			}
			ctx := logging.WithTrace(c.Request().Context(), traceID)
			c.SetRequest(c.Request().WithContext(ctx))
			c.Response().Header().Set("X-Trace-ID", traceID)
			return next(c)
		}
	}
}

// ValidTraceID accepts short opaque identifiers only.
func ValidTraceID(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// RequestLog records one structured completion line per request.
func RequestLog() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			res := c.Response()
			if err != nil && !res.Committed {
				// Let the global error handler write the envelope.
				c.Error(err)
			}
			req := c.Request()
			event := logging.From(req.Context()).Info()
			if res.Status >= 500 {
				event = logging.From(req.Context()).Error()
			} else if res.Status >= 400 {
				event = logging.From(req.Context()).Warn()
			}
			event.
				Str("method", req.Method).
				Str("path", req.URL.Path).
				Str("route", c.Path()).
				Int("status", res.Status).
				Float64("duration_ms", float64(time.Since(start).Microseconds())/1000).
				Int64("response_bytes", res.Size).
				Int64("request_bytes", req.ContentLength).
				Str("remote_ip", c.RealIP()).
				Str("user_agent", req.UserAgent()).
				Msg("http request completed")
			return nil
		}
	}
}

// Auth requires a valid API token or browser session. There is no open mode;
// an installation without accounts still requires a session.
func Auth(authenticator Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if authenticator == nil {
			return next
		}
		return func(c echo.Context) error {
			req := c.Request()
			bearer := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if bearer == "" {
				bearer = req.Header.Get("X-API-Token")
			}
			session := ""
			if cookie, err := c.Cookie(auth.SessionCookie); err == nil {
				session = cookie.Value
			}
			identity, err := authenticator.Authenticate(req.Context(), bearer, session)
			if err != nil {
				if errors.Is(err, auth.ErrUnauthorized) {
					return echo.NewHTTPError(http.StatusUnauthorized, "sign in or provide a valid API token")
				}
				return echo.NewHTTPError(http.StatusServiceUnavailable, "authentication is temporarily unavailable")
			}
			if isMutating(req.Method) && identity.Kind == auth.KindSession {
				if !authenticator.CheckCSRF(identity, req.Header.Get(auth.CSRFHeader)) {
					return echo.NewHTTPError(http.StatusForbidden, "invalid CSRF token")
				}
			}
			ctx := auth.WithIdentity(req.Context(), identity)
			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// RequireAdmin restricts a route to administrators.
func RequireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			identity, ok := auth.IdentityFrom(c.Request().Context())
			if !ok || !identity.IsAdmin() {
				return echo.NewHTTPError(http.StatusForbidden, "administrator role required")
			}
			return next(c)
		}
	}
}

// BodyLimit bounds request bodies to protect the coordinator.
func BodyLimit(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, limit)
			return next(c)
		}
	}
}
