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
	// live is the live-class keys declared so far. Their source is keyed on the
	// declared keys, so it is only reachable after discovery — after the gate
	// has ruled — and the store has no value to hold for one at gate time.
	live map[string]struct{}
}

func newFlatValues(values map[string]string) *flatValues {
	return &flatValues{values: values, scopes: map[string][]string{}, live: map[string]struct{}{}}
}

// Declare records the folders a declaration scopes its key to and whether its
// class is live, so the cells List reports next cover exactly what that
// declaration requires. Two
// declarations of one key contribute both their scopes: each of them requires
// its own folders, so keeping only the last would check fewer cells than the
// project needs.
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

// List reports one cell per entry — the project root, or one per folder in the
// key's scope. Environment is always empty: dev has no named environments, and
// a non-empty one would be filed as an override the class-wide value still has
// to back.
//
// A declared live key gets a cell whether or not the store holds a value for
// it. Its source is only reachable once the declared keys are known, which is
// after the gate has ruled, so refusing for its absence would refuse every
// project that has one. The cell states presence and nothing more: Reveal still
// answers with nothing, and no plaintext is invented.
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
	sort.Strings(keys)

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

// Reveal answers every folder of a key with the same plaintext, which is the
// broadcast itself. A row's environment is ignored because dev has no named
// environments: the gate never resolves one here, and a flat file has nowhere
// to hold an override anyway.
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
