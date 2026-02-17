package crypto

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

const masterKeyEnv = "RURIKO_MASTER_KEY"

// LoadMasterKey reads the master encryption key from the environment.
//
// The RURIKO_MASTER_KEY environment variable must be a 64-character hex string
// (32 bytes / 256 bits). Generate one with:
//
//	openssl rand -hex 32
func LoadMasterKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(masterKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("environment variable %s is not set", masterKeyEnv)
	}

	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid hex in %s: %w", masterKeyEnv, err)
	}

	if len(key) != KeySize {
		return nil, fmt.Errorf("%s must be %d bytes (%d hex chars), got %d bytes",
			masterKeyEnv, KeySize, KeySize*2, len(key))
	}

	return key, nil
}

// MustLoadMasterKey is like LoadMasterKey but panics on error.
// Use only in main() after validation.
func MustLoadMasterKey() []byte {
	key, err := LoadMasterKey()
	if err != nil {
		panic(fmt.Sprintf("failed to load master key: %v", err))
	}
	return key
}
