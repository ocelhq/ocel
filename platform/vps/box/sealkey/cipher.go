package sealkey

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	sealKeyFile    = "seal.key"
	sealKeyStaging = ".staged"
	sealKeyBytes   = 32
	sealKeyMode    = 0o400
	sealNonce      = 12
	sealTag        = 16
)

func Path(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), sealKeyFile)
}

type Cipher struct{ Root string }

func (s Cipher) Seal(_ context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	key, err := s.keyFor(tier, bound)
	if err != nil {
		return nil, err
	}
	return SealBytes(key, bound.Bytes(), plaintext)
}

func (s Cipher) Open(_ context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	key, err := s.keyFor(tier, bound)
	if err != nil {
		return nil, err
	}
	return Open(key, bound, sealed)
}

func (s Cipher) keyFor(tier environment.Tier, bound seal.AssociatedData) ([]byte, error) {
	if tier == "" {
		return nil, fmt.Errorf("a value bound to %s names no tier", bound.Bytes())
	}
	return Read(s.Root, tier)
}

func Read(root string, tier environment.Tier) ([]byte, error) {
	key, err := os.ReadFile(Path(root, tier))
	if err != nil {
		return nil, fmt.Errorf("read the %s seal key: %w", tier, err)
	}
	return key, nil
}

func SealBytes(key, associatedData, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, sealNonce)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("draw a nonce: %w", err)
	}
	return sealWithNonce(key, nonce, associatedData, plaintext)
}

func sealWithNonce(key, nonce, associatedData, plaintext []byte) ([]byte, error) {
	gcm, err := gcmOf(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(append([]byte{}, nonce...), nonce, plaintext, associatedData), nil
}

func Open(key []byte, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	return OpenBytes(key, bound.Bytes(), sealed)
}

func OpenBytes(key, associatedData, sealed []byte) ([]byte, error) {
	gcm, err := gcmOf(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < sealNonce+sealTag {
		return nil, errors.New("the sealed value is too short")
	}
	plaintext, err := gcm.Open(nil, sealed[:sealNonce], sealed[sealNonce:], associatedData)
	if err != nil {
		return nil, fmt.Errorf("the value was not sealed under this key bound to %s", associatedData)
	}
	return plaintext, nil
}

func gcmOf(key []byte) (cipher.AEAD, error) {
	if len(key) != sealKeyBytes {
		return nil, fmt.Errorf("the seal key is %d bytes, want %d", len(key), sealKeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Create(root string, tier environment.Tier) error {
	path := Path(root, tier)
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s already exists", path)
	}
	staged := path + sealKeyStaging
	if err := os.Remove(staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := writeKey(staged); err != nil {
		_ = os.Remove(staged)
		return err
	}
	defer os.Remove(staged)
	if err := os.Link(staged, path); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s was created concurrently", path)
	} else if err != nil {
		return err
	}
	return nil
}

func writeKey(staged string) error {
	key := make([]byte, sealKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("draw a seal key: %w", err)
	}
	opened, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sealKeyMode)
	if err != nil {
		return err
	}
	_, err = opened.Write(key)
	if err = errors.Join(err, opened.Sync(), opened.Close()); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(staged, 0, 0); err != nil {
			return err
		}
	}
	return os.Chmod(staged, sealKeyMode)
}

var _ seal.Cipher = Cipher{}
