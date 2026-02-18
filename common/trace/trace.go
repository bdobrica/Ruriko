// Package trace provides trace ID generation and context propagation for
// request correlation across handler → sub-operation boundaries.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// traceKey is the unexported context key used to store the trace ID.
type traceKey struct{}

// GenerateID generates a unique trace ID
func GenerateID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID if random fails (should never happen)
		return fmt.Sprintf("trace_%d", time.Now().UnixNano())
	}
	return "t_" + hex.EncodeToString(bytes)
}

// WithTraceID returns a child context carrying the given trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

// FromContext extracts the trace ID from ctx, returning "" if absent.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceKey{}).(string); ok {
		return v
	}
	return ""
}
