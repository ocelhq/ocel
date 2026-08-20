package devserver

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type flatValues struct {
	values map[string]string

	mu     sync.Mutex
	scopes map[string][]string
	live   map[string]struct{}
}

func newFlatValues(values map[string]string) *flatValues {
	return &flatValues{values: values, scopes: map[string][]string{}, live: map[string]struct{}{}}
}

func (v *flatValues) Declare(definitions []*resourcesv1.VariableDefinition) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, definition := range definitions {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			v.live[definition.GetKey()] = struct{}{}
		}
		for _, folder := range definition.GetFolders() {
			if !slices.Contains(v.scopes[definition.GetKey()], folder) {
				v.scopes[definition.GetKey()] = append(v.scopes[definition.GetKey()], folder)
			}
		}
	}
}

func (v *flatValues) List(context.Context) ([]envgate.Stored, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	keys := make([]string, 0, len(v.values)+len(v.live))
	for key := range v.values {
		keys = append(keys, key)
	}
	for key := range v.live {
		if _, held := v.values[key]; !held {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	var stored []envgate.Stored
	for _, key := range keys {
		folders := slices.Sorted(slices.Values(v.scopes[key]))
		if len(folders) == 0 {
			stored = append(stored, envgate.Stored{Address: envgate.Address{Cell: envgate.Cell{Key: key}}})
			continue
		}
		for _, folder := range folders {
			stored = append(stored, envgate.Stored{Address: envgate.Address{Cell: envgate.Cell{Key: key, Folder: folder}}})
		}
	}
	return stored, nil
}

func (v *flatValues) Reveal(_ context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	found := make(map[envgate.Cell]string, len(rows))
	for _, row := range rows {
		if value, ok := v.values[row.Cell.Key]; ok {
			found[row.Cell] = value
		}
	}
	return found, nil
}
