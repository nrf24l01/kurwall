// Package response writes the standard Kurwall HTTP envelope.
package response

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/logging"
)

// Envelope is the only response shape produced by the coordinator HTTP API.
type Envelope struct {
	Status      int                 `json:"status"`
	Code        string              `json:"code"`
	Message     string              `json:"message"`
	TraceID     string              `json:"traceId"`
	Timestamp   string              `json:"timestamp"`
	Path        string              `json:"path"`
	Data        any                 `json:"data,omitempty"`
	FieldErrors []apperr.FieldError `json:"fieldErrors,omitempty"`
}

// OK writes a 200 response.
func OK(c echo.Context, data any) error {
	return write(c, http.StatusOK, "OK", "success", data, nil)
}

// Created writes a 201 response.
func Created(c echo.Context, data any) error {
	return write(c, http.StatusCreated, "CREATED", "created", data, nil)
}

// NoContentData writes a successful response for destructive operations.
func NoContentData(c echo.Context, data any) error {
	return write(c, http.StatusOK, "OK", "success", data, nil)
}

// Error maps any error to the envelope. Unexpected errors never leak internals.
func Error(c echo.Context, err error) error {
	if err == nil {
		return OK(c, nil)
	}
	logger := logging.From(c.Request().Context())
	if appErr, ok := apperr.As(err); ok {
		if appErr.Status >= 500 {
			logError(logger, c, appErr, err)
		}
		return write(c, appErr.Status, appErr.Code, appErr.Message, nil, appErr.Fields)
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		code := "HTTP_ERROR"
		switch httpErr.Code {
		case http.StatusNotFound:
			code = "NOT_FOUND"
		case http.StatusMethodNotAllowed:
			code = "METHOD_NOT_ALLOWED"
		case http.StatusRequestEntityTooLarge:
			code = "PAYLOAD_TOO_LARGE"
		case http.StatusUnauthorized:
			code = "UNAUTHORIZED"
		case http.StatusForbidden:
			code = "FORBIDDEN"
		}
		message := http.StatusText(httpErr.Code)
		if text, ok := httpErr.Message.(string); ok && text != "" {
			message = text
		}
		if message == "" {
			message = "request failed"
		}
		return write(c, httpErr.Code, code, message, nil, nil)
	}
	logError(logger, c, nil, err)
	return write(c, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", nil, nil)
}

func logError(logger *zerolog.Logger, c echo.Context, appErr *apperr.Error, err error) {
	event := logger.Error()
	event = event.Str("method", c.Request().Method).Str("path", c.Request().URL.Path)
	if appErr != nil {
		event = event.Str("code", appErr.Code).Int("status", appErr.Status)
	}
	event.Err(err).Msg("http request failed")
}

func write(c echo.Context, status int, code, message string, data any, fields []apperr.FieldError) error {
	traceID := logging.TraceID(c.Request().Context())
	c.Response().Header().Set("X-Trace-ID", traceID)
	envelope := Envelope{
		Status:      status,
		Code:        code,
		Message:     message,
		TraceID:     traceID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Path:        c.Request().URL.Path,
		Data:        data,
		FieldErrors: fields,
	}
	return c.JSON(status, envelope)
}
