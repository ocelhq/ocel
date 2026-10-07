package livedir

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var written = struct {
	mu   sync.Mutex
	dirs map[string]bool
}{dirs: map[string]bool{}}

func RefuseUnnamableKeys(values map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if key == "" || strings.ContainsAny(key, `/\`) || strings.HasPrefix(key, ".") {
			return fmt.Errorf("%q names no file a live dir can hold; rename it where it is declared", key)
		}
	}
	return nil
}

func Write(parent, pattern string, values map[string]string) (string, error) {
	if err := RefuseUnnamableKeys(values); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", fmt.Errorf("create a live dir: %w", err)
	}
	record(dir)
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(values[key]), 0o600); err != nil {
			return "", errors.Join(fmt.Errorf("write %s into a live dir: %w", key, err), Remove(dir))
		}
	}
	return dir, nil
}

func Remove(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the live dir %s: %w", dir, err)
	}
	written.mu.Lock()
	defer written.mu.Unlock()
	for recorded := range written.dirs {
		if within, err := filepath.Rel(dir, recorded); err == nil && filepath.IsLocal(within) {
			delete(written.dirs, recorded)
		}
	}
	return nil
}

func RemoveAll() {
	written.mu.Lock()
	dirs := slices.Collect(maps.Keys(written.dirs))
	written.mu.Unlock()
	for _, dir := range dirs {
		_ = Remove(dir)
	}
}

func record(dir string) {
	written.mu.Lock()
	defer written.mu.Unlock()
	written.dirs[dir] = true
}
