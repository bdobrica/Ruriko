// Package redact provides helpers for stripping sensitive values from log
// output and structured data before it leaves the process boundary.
//
// # Threat model
//
// Secrets (API keys, Matrix access tokens, etc.) must never appear in:
//   - Log lines emitted by Ruriko or Gitai
//   - Audit payloads stored in SQLite (except the encrypted blob)
//   - Matrix room messages
//
// Redaction is best-effort: it operates on string representations and relies
// on callers to pass the right set of sensitive terms.  It is NOT a substitute
// for keeping secrets out of log call-sites in the first place.
package redact

import (
	"strings"
)

const placeholder = "[REDACTED]"

// String replaces every occurrence of each sensitive value in s with
// [REDACTED].  Values shorter than 4 characters are skipped to avoid
// spurious redaction of common substrings.
//
// Example:
//
//	safe := redact.String(logLine, apiKey, matrixToken)
func String(s string, sensitiveValues ...string) string {
	for _, v := range sensitiveValues {
		if len(v) < 4 {
			continue
		}
		s = strings.ReplaceAll(s, v, placeholder)
	}
	return s
}

// Map returns a shallow copy of m with values replaced by [REDACTED] for
// every key whose name suggests it contains a secret (password, token, key,
// secret, credential, auth).  Non-string values are left unchanged.
func Map(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			if str, ok := v.(string); ok && str != "" {
				out[k] = placeholder
				continue
			}
		}
		out[k] = v
	}
	return out
}

// isSensitiveKey returns true when the key name suggests it holds a secret.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, word := range []string{"password", "passwd", "token", "secret", "key", "credential", "auth", "apikey"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}
