package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func computeHMACSHA256Header(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return SignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateHMACSHA256_Valid(t *testing.T) {
	secret := []byte("secret")
	body := []byte(`{"a":1}`)
	sig := computeHMACSHA256Header(secret, body)

	if err := ValidateHMACSHA256(secret, body, sig); err != nil {
		t.Fatalf("expected valid signature, got error: %v", err)
	}
}

func TestValidateHMACSHA256_MissingHeader(t *testing.T) {
	err := ValidateHMACSHA256([]byte("s"), []byte("{}"), "")
	if !errors.Is(err, ErrMissingSignatureHeader) {
		t.Fatalf("expected ErrMissingSignatureHeader, got %v", err)
	}
}

func TestValidateHMACSHA256_MalformedPrefix(t *testing.T) {
	err := ValidateHMACSHA256([]byte("s"), []byte("{}"), "deadbeef")
	if !errors.Is(err, ErrMalformedSignatureHeader) {
		t.Fatalf("expected ErrMalformedSignatureHeader, got %v", err)
	}
}

func TestValidateHMACSHA256_InvalidHex(t *testing.T) {
	err := ValidateHMACSHA256([]byte("s"), []byte("{}"), SignaturePrefix+"zz")
	if !errors.Is(err, ErrInvalidSignatureHex) {
		t.Fatalf("expected ErrInvalidSignatureHex, got %v", err)
	}
}

func TestValidateHMACSHA256_Mismatch(t *testing.T) {
	secret := []byte("secret")
	body := []byte(`{"a":1}`)
	sig := computeHMACSHA256Header([]byte("other"), body)

	err := ValidateHMACSHA256(secret, body, sig)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifyHMACSHA256(t *testing.T) {
	secret := []byte("secret")
	body := []byte(`{"a":1}`)
	good := computeHMACSHA256Header(secret, body)
	bad := computeHMACSHA256Header([]byte("other"), body)

	if !VerifyHMACSHA256(secret, body, good) {
		t.Fatal("expected good signature to verify")
	}
	if VerifyHMACSHA256(secret, body, bad) {
		t.Fatal("expected bad signature to fail verification")
	}
}
