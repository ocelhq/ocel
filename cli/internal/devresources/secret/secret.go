package secret

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Purpose struct {
	Owner string
	Noun  string
}

func (p Purpose) String() string {
	return p.Owner + " " + p.Noun
}

func Ensure(path string, purpose Purpose) (string, error) {
	for {
		recorded, err := os.ReadFile(path)
		if err == nil && len(recorded) == 0 {
			return "", fmt.Errorf("%s contains no %s: run `ocel dev --reset` to start this project's %s over", path, purpose.Noun, purpose.Owner)
		}
		if err == nil {
			return string(recorded), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read this project's %s: %w", purpose, err)
		}
		if err := create(path, purpose); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
}

func create(path string, purpose Purpose) error {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate a %s: %w", purpose, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record this project's %s: %w", purpose, err)
	}
	writing, err := os.CreateTemp(filepath.Dir(path), ".writing-*")
	if err != nil {
		return fmt.Errorf("record this project's %s: %w", purpose, err)
	}
	defer func() { _ = os.Remove(writing.Name()) }()
	if _, err := writing.WriteString(hex.EncodeToString(raw)); err != nil {
		_ = writing.Close()
		return fmt.Errorf("record this project's %s: %w", purpose, err)
	}
	if err := writing.Close(); err != nil {
		return fmt.Errorf("record this project's %s: %w", purpose, err)
	}
	if err := os.Link(writing.Name(), path); err != nil {
		return fmt.Errorf("record this project's %s: %w", purpose, err)
	}
	return nil
}
