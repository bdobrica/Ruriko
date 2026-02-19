package crypto

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// ParseMasterKey decodes a 64-character hex string (32 bytes / 256 bits) into
// a raw key suitable for use with the AES-GCM helpers in this package.
//
// This function is a pure library function with no environment dependencies.
// Callers are responsible for reading the key material from env or config.
//
// Generate a suitable key with:
//
//	openssl rand -hex 32
func ParseMasterKey(rawHex string) ([]byte, error) {
	raw := strings.TrimSpace(rawHex)
	if raw == "" {
		return nil, fmt.Errorf("master key is empty")
	}

	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid hex in master key: %w", err)
	}

	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes (%d hex chars), got %d bytes",
			KeySize, KeySize*2, len(key))
	}

	return key, nil
}
