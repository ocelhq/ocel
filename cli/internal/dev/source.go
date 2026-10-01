package dev

import (
	"context"
	"fmt"
	"maps"
	"os"

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/envsource"
)

type valueSource struct {
	id       string
	ownStore bool
	values   map[string]string
}

func readValueSource(ctx context.Context, cfg *project.Project) (valueSource, error) {
	descriptor := cfg.EnvSource.Dev
	if descriptor.Reading() == envsource.ReadingOwnStore {
		return valueSource{id: descriptor.ID(), ownStore: true}, nil
	}
	source, err := descriptor.Open(cfg.Dir, os.LookupEnv)
	if err != nil {
		return valueSource{}, err
	}
	folders := []string{""}
	if folder := project.SharedFolder(cfg.Apps); folder != "" {
		folders = append(folders, folder)
	}
	read, err := source.Read(ctx, folders)
	if err != nil {
		return valueSource{}, fmt.Errorf("read envSource.dev (%s): %w", descriptor.ID(), err)
	}
	values := map[string]string{}
	for _, folder := range folders {
		for cell, value := range read {
			if cell.Folder == folder {
				values[cell.Key] = string(value.Plaintext)
			}
		}
	}
	return valueSource{id: descriptor.ID(), values: values}, nil
}

func (s valueSource) files() []string {
	if s.ownStore {
		return []string{dotfile.FileName, dotfile.LocalFileName}
	}
	return []string{dotfile.LocalFileName}
}

func (s valueSource) where() string {
	if s.ownStore {
		return dotfile.FileName
	}
	return s.id + " and " + dotfile.LocalFileName
}

func (s valueSource) remedy(key string) string {
	if s.ownStore {
		return fmt.Sprintf("add %s=<VALUE> to %s", key, dotfile.FileName)
	}
	return fmt.Sprintf("set %s in %s, or add %s=<VALUE> to %s", key, s.id, key, dotfile.LocalFileName)
}

type valueLayer struct {
	from       string
	file       bool
	values     map[string]string
	unreadable []int
}

type valueLayers []valueLayer

func (s valueSource) read(dir string) (valueLayers, error) {
	var layers valueLayers
	if s.ownStore {
		shared, err := dotfile.Load(dir)
		if err != nil {
			return nil, err
		}
		layers = append(layers, valueLayer{from: dotfile.FileName, file: true, values: shared.Values, unreadable: shared.Unreadable})
	} else {
		layers = append(layers, valueLayer{from: s.id, values: s.values})
	}
	local, err := dotfile.LoadLocal(dir)
	if err != nil {
		return nil, err
	}
	return append(layers, valueLayer{from: dotfile.LocalFileName, file: true, values: local.Values, unreadable: local.Unreadable}), nil
}

func (v valueLayers) merged() map[string]string {
	merged := map[string]string{}
	for _, layer := range v {
		maps.Copy(merged, layer.values)
	}
	return merged
}

func (v valueLayers) keys() map[string]struct{} {
	keys := map[string]struct{}{}
	for _, layer := range v {
		for key := range layer.values {
			keys[key] = struct{}{}
		}
	}
	return keys
}
