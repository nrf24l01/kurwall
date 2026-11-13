package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/coordinator/service"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login verifies credentials and starts a browser session.
func (h *Handler) Login(c echo.Context) error {
	var body loginRequest
	if err := decodeJSON(c, &body); err != nil {
		return response.Error(c, err)
	}
	token, csrf, user, err := h.Auth.Login(c.Request().Context(), body.Username, body.Password,
		c.Request().UserAgent(), c.RealIP())
	if err != nil {
		switch {
		case errors.Is(err, service.ErrRateLimited):
			return response.Error(c, apperr.New("TOO_MANY_ATTEMPTS", http.StatusTooManyRequests,
				"too many sign-in attempts; try again later"))
		case errors.Is(err, auth.ErrUnauthorized):
			return response.Error(c, apperr.New("INVALID_CREDENTIALS", http.StatusUnauthorized,
				"invalid username or password"))
		default:
			return response.Error(c, err)
		}
	}
	h.setSessionCookies(c, token, csrf)
	return response.OK(c, map[string]any{"user": map[string]any{
		"id": user.ID, "username": user.Username, "role": user.Role, "kind": auth.KindSession,
	}})
}

// Me returns the current caller and authentication mode.
func (h *Handler) Me(c echo.Context) error {
	identity, ok := auth.IdentityFrom(c.Request().Context())
	if !ok {
		return response.Error(c, apperr.New("UNAUTHORIZED", http.StatusUnauthorized, "authentication required"))
	}
	mode := "users"
	if h.Auth != nil {
		if current, err := h.Auth.Mode(c.Request().Context()); err == nil {
			mode = current
		}
	}
	return response.OK(c, map[string]any{
		"authenticated": true,
		"authMode":      mode,
		"user": map[string]any{
			"id":       identity.UserID,
			"username": identity.Username,
			"role":     identity.Role,
			"kind":     identity.Kind,
		},
	})
}

// Logout deletes the current browser session.
func (h *Handler) Logout(c echo.Context) error {
	if cookie, err := c.Cookie(auth.SessionCookie); err == nil {
		h.Auth.Logout(c.Request().Context(), cookie.Value)
	}
	h.clearSessionCookies(c)
	return response.OK(c, map[string]any{"loggedOut": true})
}

func (h *Handler) setSessionCookies(c echo.Context, sessionToken, csrfToken string) {
	secure := h.secureCookies(c)
	maxAge := int(auth.SessionTTL / time.Second)
	http.SetCookie(c.Response(), &http.Cookie{
		Name: auth.SessionCookie, Value: sessionToken, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	})
	http.SetCookie(c.Response(), &http.Cookie{
		Name: auth.CSRFCookie, Value: csrfToken, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	})
}

func (h *Handler) clearSessionCookies(c echo.Context) {
	secure := h.secureCookies(c)
	for _, name := range []string{auth.SessionCookie, auth.CSRFCookie} {
		http.SetCookie(c.Response(), &http.Cookie{
			Name: name, Value: "", Path: "/",
			HttpOnly: name == auth.SessionCookie, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
		})
	}
}

func (h *Handler) secureCookies(c echo.Context) bool {
	if c.Request().TLS != nil {
		return true
	}
	if strings.EqualFold(firstForwarded(c, "X-Forwarded-Proto"), "https") {
		return true
	}
	return strings.HasPrefix(h.PublicURL, "https://")
}
