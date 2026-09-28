package live

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	sealKeyFile  = "seal.key"
	sealKeyBytes = 32
	sealNonce    = 12
	sealTag      = 16
)

func KeyPath(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), sealKeyFile)
}

type Cipher struct{ Root string }

func (Cipher) Seal(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return nil, errors.New("the box seals values only through its helper")
}

func (s Cipher) Open(_ context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if tier == "" {
		return nil, fmt.Errorf("a value bound to %s names no tier", bound.Bytes())
	}
	key, err := os.ReadFile(KeyPath(s.Root, tier))
	if err != nil {
		return nil, fmt.Errorf("read the %s seal key: %w", tier, err)
	}
	return Open(key, bound, sealed)
}

func Open(key []byte, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if len(key) != sealKeyBytes {
		return nil, fmt.Errorf("the seal key is %d bytes, want %d", len(key), sealKeyBytes)
	}
	if len(sealed) < sealNonce+sealTag {
		return nil, errors.New("the sealed value is too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, sealed[:sealNonce], sealed[sealNonce:], bound.Bytes())
	if err != nil {
		return nil, fmt.Errorf("the value was not sealed under this key bound to %s", bound.Bytes())
	}
	return plaintext, nil
}

var _ seal.Cipher = Cipher{}
