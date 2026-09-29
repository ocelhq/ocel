package live

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type storeSource struct {
	reader   variablestore.EnvironmentReader
	cells    []variablestore.Cell
	bindings []live.Binding
}

func (f *storeSource) Fetch(ctx context.Context) (map[string]string, error) {
	resolved, err := f.reader.Values(ctx, f.cells)
	if err != nil {
		return nil, err
	}
	stored, err := f.reader.Bindings(ctx, bindingNames(f.bindings))
	if err != nil {
		return nil, err
	}
	return merged(resolved, f.bindings, stored), nil
}

func bindingNames(bindings []live.Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, l := range bindings {
		names = append(names, l.Name)
	}
	return names
}

func merged(resolved map[string]string, bindings []live.Binding, stored []variablestore.StoredBinding) map[string]string {
	out := make(map[string]string, len(resolved)+len(stored))
	maps.Copy(out, resolved)
	for i, record := range stored {
		out[bindings[i].Key] = string(record.Value)
	}
	return out
}

func FromManifest(raw []byte) (*live.Values, error) {
	manifest, err := variables.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !manifest.Live() {
		return nil, nil
	}
	clients := &ports.Clients{
		Namespace: provider.Namespace(manifest.Namespace),
		Project:   manifest.Project,
		Region:    manifest.Region,
		Endpoint:  manifest.Endpoint,
	}
	return Over(manifest, ports.KeyValues{Clients: clients}, ports.Cipher{Clients: clients}), nil
}

func Over(manifest variables.Manifest, store keyvalue.Store, cipher seal.Cipher) *live.Values {
	return live.New(&storeSource{
		reader: variablestore.EnvironmentReader{
			KeyValues:   store,
			Cipher:      cipher,
			Scope:       variablestore.Scope{Project: manifest.Slug, Tier: environment.Tier(manifest.Tier)},
			Environment: manifest.Environment,
		},
		cells:    manifestCells(manifest),
		bindings: manifest.Bindings,
	}, live.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}

func manifestCells(m variables.Manifest) []variablestore.Cell {
	cells := make([]variablestore.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, variablestore.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}
