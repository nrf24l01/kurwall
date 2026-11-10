// Package logging configures structured JSON logging for Kurwall services.
package logging

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type traceKey struct{}

// New builds a logger with a consistent service field. Console output is
// available for local development, JSON is the production default.
func New(service, level, format string, out io.Writer) zerolog.Logger {
	if out == nil {
		out = os.Stdout
	}
	lvl, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil || lvl == zerolog.NoLevel {
		lvl = zerolog.InfoLevel
	}
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.TimestampFieldName = "timestamp"
	zerolog.LevelFieldName = "level"
	zerolog.MessageFieldName = "message"

	var logger zerolog.Logger
	if strings.EqualFold(format, "console") {
		logger = zerolog.New(zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339})
	} else {
		logger = zerolog.New(out)
	}
	return logger.Level(lvl).With().Timestamp().Str("service", service).Logger()
}

// WithTrace stores the request trace ID in ctx and its logger.
func WithTrace(ctx context.Context, traceID string) context.Context {
	ctx = context.WithValue(ctx, traceKey{}, traceID)
	logger := zerolog.Ctx(ctx).With().Str("trace_id", traceID).Logger()
	return logger.WithContext(ctx)
}

// WithLogger stores a logger in ctx.
func WithLogger(ctx context.Context, logger zerolog.Logger) context.Context {
	return logger.WithContext(ctx)
}

// TraceID returns the trace ID attached by the HTTP or gRPC layer.
func TraceID(ctx context.Context) string {
	v, _ := ctx.Value(traceKey{}).(string)
	return v
}

// From returns the context logger, falling back to a disabled logger.
func From(ctx context.Context) *zerolog.Logger {
	return zerolog.Ctx(ctx)
}
