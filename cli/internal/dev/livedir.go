package dev

import (
	"maps"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/livedir"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type liveDir struct {
	root     string
	current  string
	retired  []string
	bindings map[string]string
}

func newLiveDir() (*liveDir, error) {
	root, err := livedir.Write("", "ocel-dev-live-", nil)
	if err != nil {
		return nil, err
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
	set, err := livedir.Write(d.root, "bindings-", bindings)
	if err != nil {
		return err
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
		_ = livedir.Remove(set)
	}
	d.retired = nil
}

func (d *liveDir) remove() {
	_ = livedir.Remove(d.root)
}
