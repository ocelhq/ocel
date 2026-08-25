package envgate

import (
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type CellState string

const (
	CellRequired  CellState = "required"
	CellOptional  CellState = "optional"
	CellForbidden CellState = "forbidden"
)

type MatrixCell struct {
	Folder string    `json:"folder"`
	State  CellState `json:"state"`
	Set    bool      `json:"set"`

	Version int64 `json:"version"`

	Overrides []Override `json:"overrides,omitempty"`

	Problem string `json:"problem,omitempty"`
}

type MatrixRow struct {
	Key   string       `json:"key"`
	Class string       `json:"class"`
	Scope []string     `json:"scope,omitempty"`
	Cells []MatrixCell `json:"cells"`
}

type AppResolution struct {
	Name    string `json:"name"`
	Folder  string `json:"folder"`
	Missing []Cell `json:"missing,omitempty"`
}

type Matrix struct {
	Columns []string        `json:"columns"`
	Rows    []MatrixRow     `json:"rows"`
	Apps    []AppResolution `json:"apps"`
}

var className = map[resourcesv1.VariableClass]string{
	resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:     "plain",
	resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE: "sensitive",
	resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:    "secret",
}

func (g *Gate) Matrix(environments []string) Matrix {
	g.mu.Lock()
	definitions := slices.Clone(g.definitions)
	apps := slices.Clone(g.scope.Apps)
	base := g.baseCells()
	resolved := g.resolvedCells()
	overrides := make(map[Cell][]Override, len(g.overrides))
	for cell, forCell := range g.overrides {
		for _, override := range forCell {
			override.Orphaned = Orphaned(environments, override.Environment)
			overrides[cell] = append(overrides[cell], override)
		}
	}
	complaints := map[Cell]string{}
	for _, problem := range g.problems {
		if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
			complaints[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = problem.GetDetail()
		}
	}
	g.mu.Unlock()

	columns := columns(definitions, apps, base, overrides)
	m := Matrix{Columns: columns}
	for _, definition := range definitions {
		row := MatrixRow{
			Key:   definition.GetKey(),
			Class: className[definition.GetClass()],
			Scope: definition.GetFolders(),
		}
		for _, folder := range columns {
			cell := Cell{Key: definition.GetKey(), Folder: folder}
			row.Cells = append(row.Cells, MatrixCell{
				Folder:    folder,
				State:     state(definition, folder),
				Set:       base.has(cell),
				Version:   base[cell],
				Overrides: overrides[cell],
				Problem:   complaints[cell],
			})
		}
		m.Rows = append(m.Rows, row)
	}
	for _, app := range apps {
		m.Apps = append(m.Apps, AppResolution{
			Name:    app.Name,
			Folder:  app.Folder,
			Missing: missing(definitions, app.Folder, resolved),
		})
	}
	return m
}

func state(definition *resourcesv1.VariableDefinition, folder string) CellState {
	if scope := definition.GetFolders(); len(scope) > 0 {
		if !slices.Contains(scope, folder) {
			return CellForbidden
		}
	} else if folder != "" {
		return CellOptional
	}
	if definition.GetRequired() {
		return CellRequired
	}
	return CellOptional
}

func missing(definitions []*resourcesv1.VariableDefinition, binding string, held heldCells) []Cell {
	var out []Cell
	for _, definition := range definitions {
		if !definition.GetRequired() {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) > 0 && !slices.Contains(scope, binding) {
			continue
		}
		if _, ok := hop(definition, binding, held); ok {
			continue
		}
		owed := Cell{Key: definition.GetKey()}
		if len(scope) > 0 {
			owed.Folder = binding
		}
		out = append(out, owed)
	}
	return out
}

func columns(definitions []*resourcesv1.VariableDefinition, apps []App, held heldCells, overrides map[Cell][]Override) []string {
	seen := map[string]bool{}
	for _, definition := range definitions {
		for _, folder := range definition.GetFolders() {
			seen[folder] = true
		}
	}
	for _, app := range apps {
		if app.Folder != "" {
			seen[app.Folder] = true
		}
	}
	for cell := range held {
		if cell.Folder != "" {
			seen[cell.Folder] = true
		}
	}
	for cell := range overrides {
		if cell.Folder != "" {
			seen[cell.Folder] = true
		}
	}

	folders := make([]string, 0, len(seen)+1)
	for folder := range seen {
		folders = append(folders, folder)
	}
	slices.Sort(folders)
	return append([]string{""}, folders...)
}

func (g *Gate) Forget(cell Cell) {
	g.mu.Lock()
	defer g.mu.Unlock()

	kept := g.problems[:0]
	for _, problem := range g.problems {
		if problem.GetKey() == cell.Key && problem.GetFolder() == cell.Folder {
			continue
		}
		kept = append(kept, problem)
	}
	g.problems = kept
}
