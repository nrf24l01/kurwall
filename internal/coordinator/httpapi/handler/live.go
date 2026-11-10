package handler

import (
	"github.com/labstack/echo/v4"

	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/platform/apperr"
)

// StreamTicket issues a short-lived WebSocket handshake ticket.
func (h *Handler) StreamTicket(c echo.Context) error {
	if h.Live == nil {
		return response.Error(c, apperr.Unavailable("LIVE_UNAVAILABLE", "live stream is unavailable", nil))
	}
	ticket, expires := h.Live.IssueTicket()
	return response.OK(c, map[string]any{"ticket": ticket, "expiresAt": expires})
}

// Stream upgrades the request to the live dashboard WebSocket.
func (h *Handler) Stream(c echo.Context) error {
	if h.Live == nil {
		return response.Error(c, apperr.Unavailable("LIVE_UNAVAILABLE", "live stream is unavailable", nil))
	}
	h.Live.Serve(c.Response(), c.Request())
	return nil
}
