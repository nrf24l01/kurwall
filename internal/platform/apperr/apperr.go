// Package apperr defines the typed application errors shared by the
// coordinator layers. Transport layers map them to the standard envelope.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// FieldError describes a single rejected input field.
type FieldError struct {
	Field         string `json:"field"`
	Issue         string `json:"issue"`
	RejectedValue string `json:"rejectedValue,omitempty"`
}

// Error is a typed, client-safe application error.
type Error struct {
	Code      string
	Status    int
	Message   string
	Fields    []FieldError
	Cause     error
	retryable bool
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Retryable indicates that the caller may retry the operation later.
func (e *Error) Retryable() bool { return e.retryable }

// Field appends a field-level validation error.
func (e *Error) Field(field, issue, rejected string) *Error {
	e.Fields = append(e.Fields, FieldError{Field: field, Issue: issue, RejectedValue: rejected})
	return e
}

// New creates an application error.
func New(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// Wrap creates an application error with a cause. The cause is only logged,
// never returned to API clients.
func Wrap(code string, status int, message string, cause error) *Error {
	return &Error{Code: code, Status: status, Message: message, Cause: cause}
}

// Validation returns a 400 validation error.
func Validation(message string) *Error {
	return New("VALIDATION_FAILED", http.StatusBadRequest, message)
}

// InvalidRequest returns a 400 malformed-request error.
func InvalidRequest(message string) *Error {
	return New("INVALID_REQUEST", http.StatusBadRequest, message)
}

// NotFound returns a 404 error with a domain-specific code.
func NotFound(code, message string) *Error {
	return New(code, http.StatusNotFound, message)
}

// Conflict returns a 409 error.
func Conflict(code, message string) *Error {
	return New(code, http.StatusConflict, message)
}

// Internal wraps an unexpected failure. The cause is logged, never exposed.
func Internal(operation string, cause error) *Error {
	return &Error{
		Code:    "INTERNAL_ERROR",
		Status:  http.StatusInternalServerError,
		Message: "internal server error",
		Cause:   fmt.Errorf("%s: %w", operation, cause),
	}
}

// Unavailable marks a dependency failure that can be retried.
func Unavailable(code, message string, cause error) *Error {
	return &Error{Code: code, Status: http.StatusServiceUnavailable, Message: message, Cause: cause, retryable: true}
}

// As extracts a typed application error.
func As(err error) (*Error, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

// IsRetryable reports whether err is a retryable application error.
func IsRetryable(err error) bool {
	if appErr, ok := As(err); ok {
		return appErr.retryable
	}
	return false
}
