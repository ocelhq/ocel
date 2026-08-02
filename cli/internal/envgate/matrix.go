package envgate

import (
	"sort"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// CellState is what a declaration permits one cell to hold. It exists so the
// rules are visible before anyone types: a forbidden cell is drawn unfillable
// rather than accepted and then refused on save.
type CellState string

const (
	CellRequired  CellState = "required"
	CellOptional  CellState = "optional"
	CellForbidden CellState = "forbidden"
)

// MatrixCell is one key in one folder.
type MatrixCell struct {
	Folder string    `json:"folder"`
	State  CellState `json:"state"`
	Set    bool      `json:"set"`

	// Version is the class-wide value's current version, zero when the cell
	// holds none. A write quotes it back so an edit made against a value
	// someone else has since replaced is refused rather than applied.
	Version int64 `json:"version"`

	// Overrides names the environments holding a value for this cell, which is
	// never the value a deploy resolves. It is shown because a cell that reads
	// empty while an override survives is a lie, not because anything here
	// reads or writes one.
	Overrides []string `json:"overrides,omitempty"`

	// Problem is the schema's own complaint about the value that is there,
	// empty when there is none. A missing cell needs no message: required and
	// unset already says it.
	Problem string `json:"problem,omitempty"`
}

// MatrixRow is one declared variable across every column.
type MatrixRow struct {
	Key   string       `json:"key"`
	Class string       `json:"class"`
	Scope []string     `json:"scope,omitempty"`
	Cells []MatrixCell `json:"cells"`
}

// AppResolution is one app's view of whether it can run. Missing names the
// cells it owes, in the folder it would have read them from, so completeness
// is something to see rather than infer.
type AppResolution struct {
	Name    string `json:"name"`
	Folder  string `json:"folder"`
	Missing []Cell `json:"missing,omitempty"`
}

// Matrix is the required-cell matrix: a row per declared variable, a column
// per folder with the project root first, and a readout per app.
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

// Matrix derives the whole matrix from what this run declared, what the store
// holds and what the declaring process complained about. It is the same
// authority the gate refuses on, presented rather than enforced, so the UI and
// the deploy can never disagree about which cell is owed.
func (g *Gate) Matrix() Matrix {
	g.mu.Lock()
	definitions := append([]*resourcesv1.VariableDefinition(nil), g.definitions...)
	apps := append([]App(nil), g.scope.Apps...)
	held := g.heldCells()
	overrides := make(map[Cell][]string, len(g.overrides))
	for cell, environments := range g.overrides {
		overrides[cell] = append([]string(nil), environments...)
	}
	complaints := map[Cell]string{}
	for _, problem := range g.problems {
		if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
			complaints[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = problem.GetDetail()
		}
	}
	g.mu.Unlock()

	columns := columns(definitions, apps, held, overrides)
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
				Set:       held.has(cell),
				Version:   held[cell],
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
			Missing: missing(definitions, app.Folder, held),
		})
	}
	return m
}

// state is the write rule CheckWritable enforces, plus whether a permitted
// cell is owed. The two are read from the same declaration so a cell drawn
// fillable is exactly one the write path accepts.
func state(definition *resourcesv1.VariableDefinition, folder string) CellState {
	if scope := definition.GetFolders(); len(scope) > 0 {
		if !contains(scope, folder) {
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

// missing is what one app cannot resolve. A key scoped away from the app's
// folder is absent from the answer rather than reported: the app was never
// meant to read it, so it is not a gap in that app's values.
func missing(definitions []*resourcesv1.VariableDefinition, binding string, held heldCells) []Cell {
	var out []Cell
	for _, definition := range definitions {
		if !definition.GetRequired() {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) > 0 && !contains(scope, binding) {
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

// columns are the project root and every folder either side names — declared
// in a scope, bound by an app, or holding a value class-wide or in some named
// environment. A folder only the store knows about still gets a column, so a
// value written before its folder was bound is visible rather than hidden. An
// override's folder earns one on the same terms: the column carries nothing
// required and nothing filled, but without it a surviving value has nowhere to
// be named.
func columns(definitions []*resourcesv1.VariableDefinition, apps []App, held heldCells, overrides map[Cell][]string) []string {
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
	sort.Strings(folders)
	return append([]string{""}, folders...)
}

// Forget drops what discovery said about one cell. It is what a UI write calls
// after replacing a value: the complaint described the value that was there,
// and keeping it would leave the matrix reporting a fault in something that no
// longer exists. Whatever is wrong with the new value is the next discovery
// run's to find.
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
