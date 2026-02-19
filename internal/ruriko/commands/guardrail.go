package commands

import "regexp"

// namedSecretPatterns matches well-known credential formats that should never
// appear in a Matrix message regardless of context.  Each pattern is
// intentionally specific (vendor prefix + sufficient length) to keep the
// false-positive rate low.
var namedSecretPatterns = []*regexp.Regexp{
	// OpenAI API key — classic and project variants
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_\-]{20,}\b`),
	// Anthropic
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`),
	// AWS access key ID
	regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
	// GitHub tokens (personal, OAuth, fine-grained)
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgho_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	// Slack tokens
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`),
	// Stripe secret / restricted / public keys
	regexp.MustCompile(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{20,}\b`),
}

// genericSecretPatterns catches high-entropy strings that are unlikely to
// appear in normal prose.  These are only checked for non-command messages to
// avoid false positives from legitimate command arguments (e.g. Gosuto YAML
// passed as a --content base64 value).
var genericSecretPatterns = []*regexp.Regexp{
	// Long base64 segment (≥48 contiguous chars from the base64 alphabet).
	// Using 48 instead of 40 avoids false positives from SHA-1 hashes (40 chars)
	// while still catching SHA-256 hashes (64 chars) and longer API tokens.
	regexp.MustCompile(`[A-Za-z0-9+/]{48,}={0,2}`),
	// Long lowercase hex (≥48 chars).  Avoids SHA-1 (40 chars) while catching
	// SHA-256 (64 chars) and other long hex tokens.
	regexp.MustCompile(`[0-9a-f]{48,}`),
}

// LooksLikeSecret reports whether body appears to contain a sensitive
// credential that has no business being in a Matrix chat message.
//
// When isCommand is true (the message begins with the bot command prefix),
// only the named vendor patterns are checked so that commands that legitimately
// embed base64 payloads (e.g. /ruriko gosuto set --content …) are not refused.
func LooksLikeSecret(body string, isCommand bool) bool {
	for _, re := range namedSecretPatterns {
		if re.MatchString(body) {
			return true
		}
	}
	if !isCommand {
		for _, re := range genericSecretPatterns {
			if re.MatchString(body) {
				return true
			}
		}
	}
	return false
}

// SecretGuardrailMessage is the reply sent when a message is rejected by the
// secret-in-chat guardrail.
const SecretGuardrailMessage = "⛔ That looks like a secret. " +
	"I won't store or process credentials from chat — they would be visible in room history. " +
	"Use `/ruriko secrets set <name>` to store secrets securely via a one-time link."
