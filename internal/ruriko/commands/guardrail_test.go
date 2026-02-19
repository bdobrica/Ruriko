package commands_test

import (
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
)

// TestLooksLikeSecret_NamedPatterns exercises the well-known vendor credential
// patterns that should be detected in both command and non-command messages.
func TestLooksLikeSecret_NamedPatterns(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"OpenAI classic key",
			"sk-abcdefghijklmnopqrstuvwxyz1234567890abcd"},
		{"OpenAI project key",
			"sk-proj-AbCdEf1234567890_abcdefghijklmnopqrstu"},
		{"Anthropic key",
			"sk-ant-api01-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghij"},
		{"AWS access key ID",
			"AKIAIOSFODNN7EXAMPLE"},
		{"GitHub personal token",
			"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"},
		{"GitHub OAuth token",
			"gho_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"},
		{"Slack bot token",
			"xoxb-1234567890-abcdefghijklmnopqrstuv"},
		{"Stripe live secret key",
			"sk_live_ABCDEFGHIJKLMNOPQRSTUVWxyz012345"},
		{"Stripe test key",
			"sk_test_ABCDEFGHIJKLMNOPQRSTUVWxyz012345"},
		// Key embedded inside a sentence
		{"OpenAI key in prose",
			"My API key is sk-abcdefghijklmnopqrstuvwxyz1234567890abcd please store it"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !commands.LooksLikeSecret(tc.body, false) {
				t.Errorf("LooksLikeSecret(%q, false) = false, want true", tc.body)
			}
			// Named patterns must also be detected in command context.
			if !commands.LooksLikeSecret(tc.body, true) {
				t.Errorf("LooksLikeSecret(%q, true) = false, want true (named pattern should always match)", tc.body)
			}
		})
	}
}

// TestLooksLikeSecret_GenericPatterns exercises the generic high-entropy
// patterns (long base64, long hex) which are only checked for non-commands.
func TestLooksLikeSecret_GenericPatterns(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"long base64 token",
			// 52 continuous base64 chars — clearly above the 48-char threshold.
			"Bearer ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"},
		{"long lowercase hex",
			// 64-char hex string (SHA-256 length) — above the 48-char threshold.
			"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Should be detected as non-command.
			if !commands.LooksLikeSecret(tc.body, false) {
				t.Errorf("LooksLikeSecret(%q, false) = false, want true", tc.body)
			}
			// Should NOT be detected as command (generic patterns skipped).
			if commands.LooksLikeSecret(tc.body, true) {
				t.Errorf("LooksLikeSecret(%q, true) = true, want false (generic pattern should be skipped for commands)", tc.body)
			}
		})
	}
}

// TestLooksLikeSecret_SafeMessages verifies that ordinary chat messages are
// not incorrectly flagged.
func TestLooksLikeSecret_SafeMessages(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"plain greeting", "Hello, how are you?"},
		{"command without secret", "/ruriko agents list"},
		{"approve command", "approve abc-123"},
		{"deny command", "deny abc-123 reason=\"not authorised\""},
		{"gosuto show command", "/ruriko gosuto show warren"},
		// Short base64 — below the 40-char threshold
		{"short base64", "dGVzdA=="},
		// A SHA-1 (40 hex chars) — below the 48-char threshold, should not match
		{"git sha1", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if commands.LooksLikeSecret(tc.body, false) {
				t.Errorf("LooksLikeSecret(%q, false) = true, want false (should not look like a secret)", tc.body)
			}
			if commands.LooksLikeSecret(tc.body, true) {
				t.Errorf("LooksLikeSecret(%q, true) = true, want false", tc.body)
			}
		})
	}
}

// TestLooksLikeSecret_GosutoBase64Command verifies that a gosuto set command
// carrying a long base64 YAML payload is NOT rejected in command context.
func TestLooksLikeSecret_GosutoBase64Command(t *testing.T) {
	// A plausible gosuto --content value: 80+ base64 chars, no sk- prefix.
	body := "/ruriko gosuto set warren --content " +
		"cGVyc29uYTogZmluYW5jaWFsIGFuYWx5c3QKbGltaXRzOiB7bWF4X3Rva2VuczogMTAwMH0="

	if commands.LooksLikeSecret(body, true) {
		t.Errorf("LooksLikeSecret(%q, true) = true; gosuto command with base64 should not be blocked", body)
	}
}

// TestSecretGuardrailMessage verifies the constant is non-empty and contains
// the expected redirect instruction.
func TestSecretGuardrailMessage(t *testing.T) {
	msg := commands.SecretGuardrailMessage
	if msg == "" {
		t.Fatal("SecretGuardrailMessage is empty")
	}
	if len(msg) < 20 {
		t.Errorf("SecretGuardrailMessage too short: %q", msg)
	}
}
