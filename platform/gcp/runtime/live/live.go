package live

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type storeSource struct {
	reader   envvars.EnvironmentReader
	cells    []envvars.Cell
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

func merged(resolved map[string]string, bindings []live.Binding, stored []envvars.StoredBinding) map[string]string {
	out := make(map[string]string, len(resolved)+len(stored))
	maps.Copy(out, resolved)
	for i, record := range stored {
		out[bindings[i].Key] = string(record.Value)
	}
	return out
}

func FromManifest(raw []byte) (*live.Values, error) {
	manifest, err := vars.Parse(raw)
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
	return Over(manifest, ports.Records{Clients: clients}, ports.Cipher{Clients: clients}), nil
}

func Over(manifest vars.Manifest, store records.Store, sealer records.Cipher) *live.Values {
	return live.New(&storeSource{
		reader: envvars.EnvironmentReader{
			Records:     store,
			Cipher:      sealer,
			Scope:       envvars.Scope{Project: manifest.Slug, Class: edge.Class(manifest.Class)},
			Environment: manifest.Environment,
		},
		cells:    manifestCells(manifest),
		bindings: manifest.Bindings,
	}, live.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}

func manifestCells(m vars.Manifest) []envvars.Cell {
	cells := make([]envvars.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, envvars.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}
