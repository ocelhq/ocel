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

type writtenDirs struct {
	mu      sync.Mutex
	dirs    map[string]bool
	removed bool
}

func newWrittenDirs() *writtenDirs {
	return &writtenDirs{dirs: map[string]bool{}}
}

var written = newWrittenDirs()

var errRemoved = errors.New("the process removed its live dirs and creates no more")

func RefuseUnnamableKeys(values map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if key == "" || strings.ContainsAny(key, `/\`) || strings.HasPrefix(key, ".") || isWindowsDeviceName(key) {
			return fmt.Errorf("%q names no file a live dir can hold; rename it where it is declared", key)
		}
	}
	return nil
}

func isWindowsDeviceName(key string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(key), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func Write(parent, pattern string, values map[string]string) (string, error) {
	if err := RefuseUnnamableKeys(values); err != nil {
		return "", err
	}
	dir, err := Create(parent, pattern)
	if err != nil {
		return "", err
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(values[key]), 0o600); err != nil {
			return "", errors.Join(fmt.Errorf("write %s into a live dir: %w", key, err), Remove(dir))
		}
	}
	return dir, nil
}

func Create(parent, pattern string) (string, error) {
	recorded := written
	recorded.mu.Lock()
	defer recorded.mu.Unlock()
	if recorded.removed {
		return "", fmt.Errorf("create a live dir: %w", errRemoved)
	}
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", fmt.Errorf("create a live dir: %w", err)
	}
	recorded.dirs[dir] = true
	return dir, nil
}

func Remove(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the live dir %s: %w", dir, err)
	}
	recorded := written
	recorded.mu.Lock()
	defer recorded.mu.Unlock()
	for kept := range recorded.dirs {
		if within, err := filepath.Rel(dir, kept); err == nil && filepath.IsLocal(within) {
			delete(recorded.dirs, kept)
		}
	}
	return nil
}

func RemoveRecorded() {
	recorded := written
	recorded.mu.Lock()
	recorded.removed = true
	dirs := slices.Collect(maps.Keys(recorded.dirs))
	recorded.mu.Unlock()
	for _, dir := range dirs {
		_ = Remove(dir)
	}
}
