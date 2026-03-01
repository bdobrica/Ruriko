// Package webhookauth provides shared webhook authentication helpers.
package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SignaturePrefix is the expected prefix for X-Hub-Signature-256 values.
const SignaturePrefix = "sha256="

var (
	ErrMissingSignatureHeader   = errors.New("missing X-Hub-Signature-256 header")
	ErrMalformedSignatureHeader = errors.New("malformed X-Hub-Signature-256 header")
	ErrInvalidSignatureHex      = errors.New("invalid hex in X-Hub-Signature-256")
	ErrSignatureMismatch        = errors.New("HMAC signature mismatch")
)

// ValidateHMACSHA256 validates sigHeader against body using secret.
//
// sigHeader must be in the form "sha256=<hex>", matching common webhook
// providers such as GitHub and Gitea.
func ValidateHMACSHA256(secret, body []byte, sigHeader string) error {
	if sigHeader == "" {
		return ErrMissingSignatureHeader
	}
	if !strings.HasPrefix(sigHeader, SignaturePrefix) {
		return fmt.Errorf("%w: expected prefix %q", ErrMalformedSignatureHeader, SignaturePrefix)
	}

	expectedHex := strings.TrimPrefix(sigHeader, SignaturePrefix)
	expected, err := hex.DecodeString(expectedHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSignatureHex, err)
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	computed := mac.Sum(nil)

	if !hmac.Equal(computed, expected) {
		return ErrSignatureMismatch
	}
	return nil
}

// VerifyHMACSHA256 is a bool-only convenience wrapper around ValidateHMACSHA256.
func VerifyHMACSHA256(secret, body []byte, sigHeader string) bool {
	return ValidateHMACSHA256(secret, body, sigHeader) == nil
}
