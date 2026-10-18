package workdir

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const runsDirName = "tmp"

type Run struct {
	id string

	mu   sync.Mutex
	dirs []string
}

func NewRun() *Run {
	return &Run{id: rand.Text()}
}

func (r *Run) Path(base string, elem ...string) string {
	return filepath.Join(append([]string{base, buildoutput.StateDir, runsDirName, r.id}, elem...)...)
}

func (r *Run) Write(base, name string, content []byte) (string, error) {
	path := r.Path(base, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	r.mu.Lock()
	if dir := r.Path(base); !slices.Contains(r.dirs, dir) {
		r.dirs = append(r.dirs, dir)
	}
	r.mu.Unlock()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func (r *Run) Remove() error {
	r.mu.Lock()
	dirs := r.dirs
	r.dirs = nil
	r.mu.Unlock()
	var errs []error
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func WriteShared(dir, name string, content []byte) (string, error) {
	sum := sha256.Sum256(content)
	path := filepath.Join(dir, hex.EncodeToString(sum[:8]), name)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	staged, err := os.CreateTemp(filepath.Dir(path), "."+name+".*")
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(staged.Name())
	if _, err := staged.Write(content); err != nil {
		_ = staged.Close()
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := staged.Close(); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(staged.Name(), path); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
