package live

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	dataLink       = "..data"
	generationLead = ".."
)

type projection struct {
	root string
	keys []string
	held string
}

func newProjection(root string, keys []string) (*projection, error) {
	for _, key := range keys {
		if key == "" || strings.ContainsAny(key, "/\\") || strings.HasPrefix(key, ".") {
			return nil, fmt.Errorf("live key %q cannot name a file under %s", key, root)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("make the live value directory: %w", err)
	}
	for _, key := range keys {
		if err := link(filepath.Join(dataLink, key), filepath.Join(root, key)); err != nil {
			return nil, err
		}
	}
	return &projection{root: root, keys: keys}, nil
}

func (p *projection) write(generation uint32, values map[string]string) error {
	dir := filepath.Join(p.root, generationLead+strconv.FormatUint(uint64(generation), 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, key := range p.keys {
		value, held := values[key]
		if !held {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0o600); err != nil {
			return err
		}
	}
	if err := link(filepath.Base(dir), filepath.Join(p.root, dataLink)); err != nil {
		return err
	}
	if p.held != "" && p.held != dir {
		_ = os.RemoveAll(p.held)
	}
	p.held = dir
	return nil
}

func link(target, at string) error {
	staged := at + ".tmp"
	_ = os.Remove(staged)
	if err := os.Symlink(target, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, at); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return nil
}
