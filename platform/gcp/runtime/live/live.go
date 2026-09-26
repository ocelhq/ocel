package live

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type storeSource struct {
	reader   values.View
	cells    []values.Cell
	bindings []live.Binding
}

func (f *storeSource) Fetch(ctx context.Context) (map[string]string, error) {
	resolved, err := f.reader.Values(ctx, f.cells)
	if err != nil {
		return nil, err
	}
	records, err := f.reader.Bindings(ctx, bindingNames(f.bindings))
	if err != nil {
		return nil, err
	}
	return merged(resolved, f.bindings, records), nil
}

func bindingNames(bindings []live.Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, l := range bindings {
		names = append(names, l.Name)
	}
	return names
}

func merged(resolved map[string]string, bindings []live.Binding, records []values.Published) map[string]string {
	out := make(map[string]string, len(resolved)+len(records))
	maps.Copy(out, resolved)
	for i, record := range records {
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
		Namespace: providerkit.Namespace(manifest.Namespace),
		Project:   manifest.Project,
		Region:    manifest.Region,
		Endpoint:  manifest.Endpoint,
	}
	return Over(manifest, ports.Records{Clients: clients}, ports.Cipher{Clients: clients}), nil
}

func Over(manifest vars.Manifest, records records.Store, sealer records.Cipher) *live.Values {
	return live.New(&storeSource{
		reader: values.View{
			Records:     records,
			Cipher:      sealer,
			Scope:       values.Scope{Project: manifest.Slug, Class: edge.Class(manifest.Class)},
			Environment: manifest.Environment,
		},
		cells:    manifestCells(manifest),
		bindings: manifest.Bindings,
	}, live.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}

func manifestCells(m vars.Manifest) []values.Cell {
	cells := make([]values.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, values.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}
