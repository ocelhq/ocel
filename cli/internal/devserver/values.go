package devserver

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type flatValues struct {
	values map[string]string

	mu         sync.Mutex
	scopes     map[string][]string
	secretKeys map[string]struct{}
}

func newFlatValues(values map[string]string) *flatValues {
	return &flatValues{values: values, scopes: map[string][]string{}, secretKeys: map[string]struct{}{}}
}

func (v *flatValues) Declare(definitions []*resourcesv1.VariableDefinition) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, definition := range definitions {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			v.secretKeys[definition.GetKey()] = struct{}{}
		}
		for _, folder := range definition.GetFolders() {
			if !slices.Contains(v.scopes[definition.GetKey()], folder) {
				v.scopes[definition.GetKey()] = append(v.scopes[definition.GetKey()], folder)
			}
		}
	}
}

func (v *flatValues) List(context.Context) ([]variables.ValueMetadata, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	keys := make([]string, 0, len(v.values)+len(v.secretKeys))
	for key := range v.values {
		keys = append(keys, key)
	}
	for key := range v.secretKeys {
		if _, ok := v.values[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	var stored []variables.ValueMetadata
	for _, key := range keys {
		folders := slices.Sorted(slices.Values(v.scopes[key]))
		if len(folders) == 0 {
			stored = append(stored, variables.ValueMetadata{Coordinate: variables.Coordinate{Cell: variables.Cell{Key: key}}})
			continue
		}
		for _, folder := range folders {
			stored = append(stored, variables.ValueMetadata{Coordinate: variables.Coordinate{Cell: variables.Cell{Key: key, Folder: folder}}})
		}
	}
	return stored, nil
}

func (v *flatValues) Reveal(_ context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	found := make(map[variables.Coordinate]string, len(rows))
	for _, row := range rows {
		if value, ok := v.values[row.Cell.Key]; ok {
			found[row] = value
		}
	}
	return found, nil
}

func (v *flatValues) sortedSecretKeys() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Sorted(maps.Keys(v.secretKeys))
}
