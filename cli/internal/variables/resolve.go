package variables

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type ResolvedValue struct {
	Folder  string
	Value   string
	Version int64
}

type resolvedCell struct {
	cell Cell
	live bool
}

func (d *Declarations) Resolve(ctx context.Context, app string) (map[string]ResolvedValue, error) {
	cells, present, err := d.cellsOf(app)
	if err != nil {
		return nil, err
	}
	var wanted []Cell
	for _, c := range cells {
		if !c.live {
			wanted = append(wanted, c.cell)
		}
	}
	plaintext, err := d.reveal(ctx, wanted)
	if err != nil {
		return nil, err
	}

	resolved := make(map[string]ResolvedValue, len(cells))
	for _, c := range cells {
		from := ResolvedValue{Folder: c.cell.Folder, Version: present[c.cell]}
		if !c.live {
			if !plaintext[c.cell].found {
				continue
			}
			from.Value = plaintext[c.cell].value
		}
		resolved[c.cell.Key] = from
	}
	return resolved, nil
}

func (d *Declarations) RevealSecrets(ctx context.Context, apps []string) (map[string]map[string]string, error) {
	secretsOf := make(map[string][]Cell, len(apps))
	for _, app := range apps {
		cells, _, err := d.cellsOf(app)
		if err != nil {
			return nil, err
		}
		for _, c := range cells {
			if c.live {
				secretsOf[app] = append(secretsOf[app], c.cell)
			}
		}
	}

	d.mu.Lock()
	var wanted []Coordinate
	at := map[Cell]Coordinate{}
	for _, cells := range secretsOf {
		for _, cell := range cells {
			if _, seen := at[cell]; seen {
				continue
			}
			at[cell] = Coordinate{Cell: cell, Environment: d.resolvedEnvironment(cell)}
			wanted = append(wanted, at[cell])
		}
	}
	d.mu.Unlock()

	var found map[Coordinate]string
	if len(wanted) > 0 {
		var err error
		found, err = d.values.Reveal(ctx, wanted)
		if err != nil {
			return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("read %s: %w", describeAll(wanted), err))
		}
	}
	revealed := make(map[string]map[string]string, len(apps))
	for _, app := range apps {
		revealed[app] = map[string]string{}
		for _, cell := range secretsOf[app] {
			if value, ok := found[at[cell]]; ok {
				revealed[app][cell.Key] = value
			}
		}
	}
	return revealed, nil
}

func (d *Declarations) cellsOf(app string) ([]resolvedCell, presentCells, error) {
	binding, known := d.binding(app)
	if !known {
		return nil, nil, fmt.Errorf("app %q is not declared in this project's config, so it has no folder to resolve from", app)
	}

	d.mu.Lock()
	present := d.resolvedCells()
	definitions := slices.Clone(d.definitions)
	d.mu.Unlock()

	cells := make([]resolvedCell, 0, len(definitions))
	for _, definition := range definitions {
		cell, ok := resolveCell(definition, binding, present)
		if !ok {
			continue
		}
		cells = append(cells, resolvedCell{cell: cell, live: definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET})
	}
	return cells, present, nil
}

func resolveCell(definition *resourcesv1.VariableDefinition, binding string, present presentCells) (Cell, bool) {
	if scope := definition.GetFolders(); len(scope) > 0 {
		if binding == "" || !slices.Contains(scope, binding) {
			return Cell{}, false
		}
		cell := Cell{Key: definition.GetKey(), Folder: binding}
		return cell, present.has(cell)
	}
	if binding != "" {
		if cell := (Cell{Key: definition.GetKey(), Folder: binding}); present.has(cell) {
			return cell, true
		}
	}
	cell := Cell{Key: definition.GetKey()}
	return cell, present.has(cell)
}

func (d *Declarations) binding(app string) (string, bool) {
	for _, a := range d.scope.Apps {
		if a.Name == app {
			return a.Folder, true
		}
	}
	return "", false
}

type presentCells map[Cell]int64

func (h presentCells) has(cell Cell) bool {
	_, ok := h[cell]
	return ok
}

func cellsOf(present presentCells, key string) []Cell {
	var out []Cell
	for cell := range present {
		if cell.Key == key {
			out = append(out, cell)
		}
	}
	slices.SortFunc(out, func(a, b Cell) int { return cmp.Compare(a.Folder, b.Folder) })
	return out
}

func (d *Declarations) baseCells() presentCells {
	cells := make(presentCells, len(d.cells))
	for _, row := range d.cells {
		cells[row.Cell] = row.Version
	}
	return cells
}

func (d *Declarations) resolvedCells() presentCells {
	cells := d.baseCells()
	if d.scope.Environment == "" {
		return cells
	}
	for cell := range d.overrides {
		if version, ok := d.ownOverride(cell); ok {
			cells[cell] = version
		}
	}
	return cells
}

func (d *Declarations) ownOverride(cell Cell) (int64, bool) {
	if d.scope.Environment == "" {
		return 0, false
	}
	for _, override := range d.overrides[cell] {
		if override.Environment == d.scope.Environment {
			return override.Version, true
		}
	}
	return 0, false
}

func (d *Declarations) resolvedEnvironment(cell Cell) string {
	if _, ok := d.ownOverride(cell); ok {
		return d.scope.Environment
	}
	return ""
}
