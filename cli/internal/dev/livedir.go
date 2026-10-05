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
	if d.bindings == nil || !maps.Equal(bindings, d.bindings) {
		if err := d.write(bindings); err != nil {
			return nil, err
		}
	}
	appEnv[processenv.LiveDirEnvVar] = d.root
	return appEnv, nil
}

func (d *liveDir) write(bindings map[string]string) error {
	for key := range bindings {
		if strings.ContainsAny(key, `/\`) || strings.HasPrefix(key, ".") {
			return fmt.Errorf("binding %q cannot name a file under %s", key, d.root)
		}
	}
	for key, value := range bindings {
		if err := d.replace(key, value); err != nil {
			return fmt.Errorf("write binding %s to %s: %w", key, d.root, err)
		}
	}
	for key := range d.bindings {
		if _, kept := bindings[key]; !kept {
			if err := os.Remove(filepath.Join(d.root, key)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove binding %s from %s: %w", key, d.root, err)
			}
		}
	}
	d.bindings = bindings
	return nil
}

func (d *liveDir) replace(key, value string) error {
	staged, err := os.CreateTemp(d.root, ".staged-")
	if err != nil {
		return err
	}
	if _, err := staged.WriteString(value); err != nil {
		_ = staged.Close()
		_ = os.Remove(staged.Name())
		return err
	}
	if err := staged.Close(); err != nil {
		_ = os.Remove(staged.Name())
		return err
	}
	if err := os.Rename(staged.Name(), filepath.Join(d.root, key)); err != nil {
		_ = os.Remove(staged.Name())
		return err
	}
	return nil
}

func (d *liveDir) remove() {
	_ = os.RemoveAll(d.root)
}
