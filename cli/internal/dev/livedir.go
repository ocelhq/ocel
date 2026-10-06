package dev

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/processenv"
)

type liveDir struct {
	root     string
	current  string
	retired  []string
	bindings map[string]string
}

func newLiveDir() (*liveDir, error) {
	root, err := os.MkdirTemp("", "ocel-dev-live-")
	if err != nil {
		return nil, fmt.Errorf("make the directory the app reads its bindings from: %w", err)
	}
	return &liveDir{root: root}, nil
}

func (d *liveDir) project(env map[string]string) (map[string]string, error) {
	bindings := map[string]string{}
	appEnv := make(map[string]string, len(env)+1)
	for key, value := range env {
		if strings.HasPrefix(key, processenv.ResourceEnvVarPrefix) {
			bindings[key] = value
		} else {
			appEnv[key] = value
		}
	}
	if d.current == "" || !maps.Equal(bindings, d.bindings) {
		if err := d.write(bindings); err != nil {
			return nil, err
		}
	}
	appEnv[processenv.LiveDirEnvVar] = d.current
	return appEnv, nil
}

func (d *liveDir) write(bindings map[string]string) error {
	for key := range bindings {
		if strings.ContainsAny(key, `/\`) || strings.HasPrefix(key, ".") {
			return fmt.Errorf("binding %q cannot name a file under %s", key, d.root)
		}
	}
	set, err := os.MkdirTemp(d.root, "bindings-")
	if err != nil {
		return fmt.Errorf("make a directory for the bindings under %s: %w", d.root, err)
	}
	for key, value := range bindings {
		if err := os.WriteFile(filepath.Join(set, key), []byte(value), 0o600); err != nil {
			_ = os.RemoveAll(set)
			return fmt.Errorf("write binding %s to %s: %w", key, set, err)
		}
	}
	if d.current != "" {
		d.retired = append(d.retired, d.current)
	}
	d.current = set
	d.bindings = bindings
	return nil
}

func (d *liveDir) retire() {
	for _, set := range d.retired {
		_ = os.RemoveAll(set)
	}
	d.retired = nil
}

func (d *liveDir) remove() {
	_ = os.RemoveAll(d.root)
}
