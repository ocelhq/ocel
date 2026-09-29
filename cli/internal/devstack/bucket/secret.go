package bucket

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const secretFile = "bucket-secret-key"

func keptSecret(stateDir string) (string, error) {
	path := filepath.Join(stateDir, secretFile)
	for {
		kept, err := os.ReadFile(path)
		if err == nil && len(kept) == 0 {
			return "", fmt.Errorf("%s contains no key: run `ocel dev --reset` to start this project's bucket store over", path)
		}
		if err == nil {
			return string(kept), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read this project's bucket store key: %w", err)
		}
		if err := keepNewSecret(path); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
}

func keepNewSecret(path string) error {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate a bucket store key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("keep this project's bucket store key: %w", err)
	}
	writing, err := os.CreateTemp(filepath.Dir(path), ".writing-*")
	if err != nil {
		return fmt.Errorf("keep this project's bucket store key: %w", err)
	}
	defer func() { _ = os.Remove(writing.Name()) }()
	if _, err := writing.WriteString(hex.EncodeToString(raw)); err != nil {
		_ = writing.Close()
		return fmt.Errorf("keep this project's bucket store key: %w", err)
	}
	if err := writing.Close(); err != nil {
		return fmt.Errorf("keep this project's bucket store key: %w", err)
	}
	if err := os.Link(writing.Name(), path); err != nil {
		return fmt.Errorf("keep this project's bucket store key: %w", err)
	}
	return nil
}
