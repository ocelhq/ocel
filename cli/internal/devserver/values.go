package devserver

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// flatValues is dev's variable store: one flat map of root values and nothing
// else. It implements envgate.Values so the gate dev runs is the gate a deploy
// runs, over a different store — the verdict, the schema check and the two-hop
// rule are all the shared code's.
//
// Its one divergence is the broadcast. A flat map has no folders in it, and a
// scoped key has no root cell to hold a value in, so a root entry stands as the
// value for every folder its declaration names. That makes a scoped key
// readable in dev at the cost of the thing folders exist for: the value cannot
// diverge per app here, and the CLI says so.
type flatValues struct {
	values map[string]string

	mu sync.Mutex
	// scopes is the folders each declared key names, learned from the
	// declarations as they arrive: it is the only place folders exist at all,
	// since the file itself has none.
	scopes map[string][]string
}

func newFlatValues(values map[string]string) *flatValues {
	return &flatValues{values: values, scopes: map[string][]string{}}
}

// Declare records the folders a declaration scopes its key to, so the cells
// List reports next cover exactly what that declaration requires. Two
// declarations of one key contribute both their scopes: each of them requires
// its own folders, so keeping only the last would check fewer cells than the
// project needs.
func (v *flatValues) Declare(definitions []*resourcesv1.VariableDefinition) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, definition := range definitions {
		for _, folder := range definition.GetFolders() {
			if !slices.Contains(v.scopes[definition.GetKey()], folder) {
				v.scopes[definition.GetKey()] = append(v.scopes[definition.GetKey()], folder)
			}
		}
	}
}

// List reports one cell per entry — the project root, or one per folder in the
// key's scope. Environment is always empty: dev has no named environments, and
// a non-empty one would be filed as an override the class-wide value still has
// to back.
func (v *flatValues) List(context.Context) ([]envgate.Stored, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	keys := make([]string, 0, len(v.values))
	for key := range v.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var stored []envgate.Stored
	for _, key := range keys {
		folders := slices.Sorted(slices.Values(v.scopes[key]))
		if len(folders) == 0 {
			stored = append(stored, envgate.Stored{Cell: envgate.Cell{Key: key}})
			continue
		}
		for _, folder := range folders {
			stored = append(stored, envgate.Stored{Cell: envgate.Cell{Key: key, Folder: folder}})
		}
	}
	return stored, nil
}

// Reveal answers every folder of a key with the same plaintext, which is the
// broadcast itself.
func (v *flatValues) Reveal(_ context.Context, cells []envgate.Cell) (map[envgate.Cell]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	found := make(map[envgate.Cell]string, len(cells))
	for _, cell := range cells {
		if value, ok := v.values[cell.Key]; ok {
			found[cell] = value
		}
	}
	return found, nil
}
