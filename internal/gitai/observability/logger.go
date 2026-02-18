// Package observability provides structured logging helpers for Gitai.
//
// It wraps log/slog with trace ID propagation and secret redaction so that
// every log line emitted during a turn carries the trace context.
package observability

import (
	"context"
	"log/slog"
	"os"

	"github.com/bdobrica/Ruriko/common/redact"
	"github.com/bdobrica/Ruriko/common/trace"
)

// Setup configures the global slog logger according to the provided level and
// format strings (e.g. level="info", format="json").
func Setup(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// WithTrace returns a child logger that always includes the trace_id from ctx.
func WithTrace(ctx context.Context) *slog.Logger {
	traceID := trace.FromContext(ctx)
	if traceID == "" {
		return slog.Default()
	}
	return slog.With("trace_id", traceID)
}

// RedactSecrets replaces known-sensitive values in a log message with "[REDACTED]".
// Call with the message text and the sensitive values to strip out.
func RedactSecrets(msg string, sensitiveValues ...string) string {
	return redact.String(msg, sensitiveValues...)
}
