package grpcapi

import (
	"context"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kurwall/kurwall/internal/platform/id"
	"github.com/kurwall/kurwall/internal/platform/logging"
)

// TraceUnaryInterceptor attaches a trace ID and structured request log.
func TraceUnaryInterceptor(base zerolog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		ctx = logging.WithLogger(ctx, base)
		ctx = withTrace(ctx)
		start := time.Now()
		defer func() {
			if r := recover(); r != nil {
				logging.From(ctx).Error().Interface("panic", r).Bytes("stack", debug.Stack()).Msg("grpc handler panicked")
				err = status.Error(codes.Internal, "internal error")
			}
			logging.From(ctx).Info().Str("grpc_method", info.FullMethod).Dur("duration_ms", time.Since(start)).
				Str("code", status.Code(err).String()).Msg("grpc request completed")
		}()
		return handler(ctx, req)
	}
}

// TraceStreamInterceptor attaches a trace ID to every stream.
func TraceStreamInterceptor(base zerolog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		ctx := logging.WithLogger(stream.Context(), base)
		ctx = withTrace(ctx)
		start := time.Now()
		wrapped := &wrappedStream{ServerStream: stream, ctx: ctx}
		defer func() {
			if r := recover(); r != nil {
				logging.From(ctx).Error().Interface("panic", r).Bytes("stack", debug.Stack()).Msg("grpc stream panicked")
				err = status.Error(codes.Internal, "internal error")
			}
			logging.From(ctx).Info().Str("grpc_method", info.FullMethod).Dur("duration_ms", time.Since(start)).
				Str("code", status.Code(err).String()).Msg("grpc stream closed")
		}()
		return handler(srv, wrapped)
	}
}

func withTrace(ctx context.Context) context.Context {
	traceID := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-trace-id"); len(values) > 0 {
			traceID = values[0]
		}
	}
	if !validTraceID(traceID) {
		traceID = id.New()
	}
	return logging.WithTrace(ctx, traceID)
}

func validTraceID(v string) bool {
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

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// TrimTraceHeader normalizes an incoming trace header.
func TrimTraceHeader(v string) string { return strings.TrimSpace(v) }
