package connectorkit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Identity struct {
	private ed25519.PrivateKey
}

func LoadOrCreateIdentity(path string) (Identity, error) {
	if path == "" {
		return Identity{}, errors.New("connectorkit: no key path, so this connector can have no identity")
	}
	read, err := os.ReadFile(path)
	switch {
	case err == nil:
		seed, decoded := base64.StdEncoding.DecodeString(strings.TrimSpace(string(read)))
		if decoded != nil {
			return Identity{}, fmt.Errorf("connector key %s is not base64: %w", path, decoded)
		}
		if len(seed) != ed25519.SeedSize {
			return Identity{}, fmt.Errorf("connector key %s is %d bytes, not %d", path, len(seed), ed25519.SeedSize)
		}
		return Identity{private: ed25519.NewKeyFromSeed(seed)}, nil
	case !errors.Is(err, os.ErrNotExist):
		return Identity{}, fmt.Errorf("read connector key: %w", err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return Identity{}, fmt.Errorf("draw a connector key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Identity{}, fmt.Errorf("make the connector key directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return Identity{}, fmt.Errorf("write connector key: %w", err)
	}
	return Identity{private: ed25519.NewKeyFromSeed(seed)}, nil
}

// IdentityFromSeed returns the identity an ed25519 seed names, for a connector
// that reads its key out of a secret store rather than off a disk of its own.
func IdentityFromSeed(seed []byte) (Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("connector key is %d bytes, not %d", len(seed), ed25519.SeedSize)
	}
	return Identity{private: ed25519.NewKeyFromSeed(seed)}, nil
}

func (i Identity) HasKey() bool {
	return i.private != nil
}

func (i Identity) PublicKey() string {
	return base64.StdEncoding.EncodeToString(i.private.Public().(ed25519.PublicKey))
}
