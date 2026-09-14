package live

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Values = rt.Values

type storeFetcher struct {
	reader   values.Reader
	cells    []values.Cell
	bindings []rt.Binding
}

func (f *storeFetcher) FetchLive(ctx context.Context) (map[string]string, error) {
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

func bindingNames(bindings []rt.Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, l := range bindings {
		names = append(names, l.Name)
	}
	return names
}

func merged(resolved map[string]string, bindings []rt.Binding, records []values.Published) map[string]string {
	out := make(map[string]string, len(resolved)+len(records))
	maps.Copy(out, resolved)
	for i, record := range records {
		out[bindings[i].Key] = string(record.Value)
	}
	return out
}

func FromManifest(raw []byte) (*Values, error) {
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
	return Over(manifest, ports.Records{Clients: clients}, ports.Sealer{Clients: clients}), nil
}

func Over(manifest vars.Manifest, records providerkit.RecordStore, sealer providerkit.Sealer) *Values {
	return rt.New(&storeFetcher{
		reader: values.Reader{
			Records:     records,
			Sealer:      sealer,
			Scope:       values.Scope{Project: manifest.Slug, Class: providerkit.Class(manifest.Class)},
			Environment: manifest.Environment,
		},
		cells:    manifestCells(manifest),
		bindings: manifest.Bindings,
	}, rt.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}

func manifestCells(m vars.Manifest) []values.Cell {
	cells := make([]values.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, values.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}
