package crypto_test

import (
	"bytes"
	"testing"

	"github.com/bdobrica/Ruriko/common/crypto"
)

func makeKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	key := makeKey(t)
	plaintext := []byte("super-secret-api-key-value-123")

	ciphertext, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}

	recovered, err := crypto.Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(recovered, plaintext) {
		t.Errorf("recovered %q, want %q", recovered, plaintext)
	}
}

func TestEncrypt_NonDeterministic(t *testing.T) {
	key := makeKey(t)
	plaintext := []byte("same plaintext")

	c1, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("first Encrypt: %v", err)
	}

	c2, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("second Encrypt: %v", err)
	}

	// Random nonce means ciphertexts should differ
	if bytes.Equal(c1, c2) {
		t.Error("two encryptions of same plaintext produced identical ciphertext (nonce not random)")
	}
}

func TestEncrypt_InvalidKeySize(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"16-byte", make([]byte, 16)},
		{"31-byte", make([]byte, 31)},
		{"33-byte", make([]byte, 33)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := crypto.Encrypt(tc.key, []byte("data"))
			if err == nil {
				t.Fatal("expected error for invalid key size, got nil")
			}
		})
	}
}

func TestDecrypt_InvalidKeySize(t *testing.T) {
	_, err := crypto.Decrypt(make([]byte, 16), make([]byte, 32))
	if err == nil {
		t.Fatal("expected error for invalid key size, got nil")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	key := makeKey(t)
	plaintext := []byte("tamper test")

	ciphertext, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a byte in the ciphertext body (after nonce)
	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err = crypto.Decrypt(key, ciphertext)
	if err == nil {
		t.Fatal("expected authentication failure for tampered ciphertext, got nil")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := makeKey(t)
	key2 := make([]byte, crypto.KeySize)
	for i := range key2 {
		key2[i] = byte(i + 100)
	}

	ciphertext, err := crypto.Encrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = crypto.Decrypt(key2, ciphertext)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key, got nil")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	key := makeKey(t)
	_, err := crypto.Decrypt(key, []byte("short"))
	if err == nil {
		t.Fatal("expected error for too-short ciphertext, got nil")
	}
}

func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := makeKey(t)

	ciphertext, err := crypto.Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	recovered, err := crypto.Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}

	if len(recovered) != 0 {
		t.Errorf("expected empty plaintext, got %q", recovered)
	}
}
