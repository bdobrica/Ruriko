// Package trace provides trace ID generation for request correlation
package trace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateID generates a unique trace ID
func GenerateID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID if random fails (should never happen)
		return fmt.Sprintf("trace_%d", time.Now().UnixNano())
	}
	return "t_" + hex.EncodeToString(bytes)
}
