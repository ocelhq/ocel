package variables

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type CellRequirement string

const (
	CellRequired  CellRequirement = "required"
	CellOptional  CellRequirement = "optional"
	CellForbidden CellRequirement = "forbidden"
)

type MatrixCell struct {
	Folder string          `json:"folder"`
	State  CellRequirement `json:"state"`
	Set    bool            `json:"set"`

	Version int64 `json:"version"`

	Overrides []Override `json:"overrides,omitempty"`

	Reference *Reference `json:"reference,omitempty"`

	Problem string `json:"problem,omitempty"`

	EnvSource string `json:"envSource,omitempty"`
}

type MatrixRow struct {
	Key         string       `json:"key"`
	Description string       `json:"description,omitempty"`
	Class       string       `json:"class"`
	Scope       []string     `json:"scope,omitempty"`
	Group       string       `json:"group,omitempty"`
	Cells       []MatrixCell `json:"cells"`
}

type MatrixGroup struct {
	Key         string `json:"key"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

type AppResolution struct {
	Name    string `json:"name"`
	Folder  string `json:"folder"`
	Missing []Cell `json:"missing,omitempty"`
}

type Matrix struct {
	Columns []string        `json:"columns"`
	Rows    []MatrixRow     `json:"rows"`
	Groups  []MatrixGroup   `json:"groups,omitempty"`
	Apps    []AppResolution `json:"apps"`

	Undeclared []UndeclaredCell `json:"undeclared,omitempty"`
}

type UndeclaredCell struct {
	Cell
	EnvSource string `json:"envSource"`
}

var classNames = map[resourcesv1.VariableClass]string{
	resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:     "plain",
	resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE: "sensitive",
	resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:    "secret",
}

func ClassName(class resourcesv1.VariableClass) string {
	return classNames[class]
}

func (d *Declarations) Matrix(environments []string) Matrix {
	d.mu.Lock()
	definitions := slices.Clone(d.definitions)
	groups := slices.Clone(d.groups)
	apps := slices.Clone(d.scope.Apps)
	base := d.baseCells()
	resolved := d.resolvedCells()
	references := maps.Clone(d.references)
	copied := map[Cell]string{}
	for _, stored := range d.cells {
		if stored.EnvSource != "" && stored.EnvSource != string(envsource.Builtin) {
			copied[stored.Cell] = stored.EnvSource
		}
	}
	overrides := make(map[Cell][]Override, len(d.overrides))
	for cell, forCell := range d.overrides {
		for _, override := range forCell {
			override.Orphaned = IsOrphaned(environments, override.Environment)
			overrides[cell] = append(overrides[cell], override)
		}
	}
	complaints := map[Cell]string{}
	for _, problem := range d.problems {
		if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
			complaints[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = cmp.Or(problem.GetDetail(), "it does not satisfy its schema")
		}
	}
	d.mu.Unlock()

	appDefinitions, appGroups := definitions, groups
	definitions = append(slices.Clone(definitions), d.scope.impliedDefinitions()...)
	groups = append(slices.Clone(groups), d.scope.impliedGroups()...)
	columns := columns(definitions, apps, base, overrides)
	m := Matrix{
		Columns: columns,
		Rows:    make([]MatrixRow, 0, len(definitions)),
		Apps:    make([]AppResolution, 0, len(apps)),
	}
	for _, definition := range definitions {
		row := MatrixRow{
			Key:         definition.GetKey(),
			Description: definition.GetDescription(),
			Class:       ClassName(definition.GetClass()),
			Scope:       definition.GetFolders(),
			Group:       definition.GetGroup(),
			Cells:       make([]MatrixCell, 0, len(columns)),
		}
		for _, folder := range columns {
			cell := Cell{Key: definition.GetKey(), Folder: folder}
			row.Cells = append(row.Cells, MatrixCell{
				Folder:    folder,
				State:     state(definition, folder),
				Set:       base.has(cell),
				Version:   base[cell],
				Overrides: overrides[cell],
				Reference: references[cell],
				Problem:   complaints[cell],
				EnvSource: copied[cell],
			})
		}
		m.Rows = append(m.Rows, row)
	}
	for _, group := range groups {
		m.Groups = append(m.Groups, MatrixGroup{
			Key:         group.GetKey(),
			Required:    group.GetRequired(),
			Description: group.GetDescription(),
		})
	}
	for cell, envSource := range copied {
		if !declares(definitions, cell) {
			m.Undeclared = append(m.Undeclared, UndeclaredCell{Cell: cell, EnvSource: envSource})
		}
	}
	slices.SortFunc(m.Undeclared, func(a, b UndeclaredCell) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Folder, b.Folder))
	})
	for _, app := range apps {
		m.Apps = append(m.Apps, AppResolution{
			Name:    app.Name,
			Folder:  app.Folder,
			Missing: missing(appDefinitions, appGroups, app.Folder, resolved),
		})
	}
	return m
}

func state(definition *resourcesv1.VariableDefinition, folder string) CellRequirement {
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

func missing(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, binding string, present presentCells) []Cell {
	var out []Cell
	for _, definition := range definitions {
		if !needsValue(definition, definitions, groups, binding, present) {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) > 0 && !slices.Contains(scope, binding) {
			continue
		}
		if resolves(definition, binding, present) {
			continue
		}
		missing := Cell{Key: definition.GetKey()}
		if len(scope) > 0 {
			missing.Folder = binding
		}
		out = append(out, missing)
	}
	return out
}

func columns(definitions []*resourcesv1.VariableDefinition, apps []App, present presentCells, overrides map[Cell][]Override) []string {
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
	for cell := range present {
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

func (d *Declarations) ClearProblems(cell Cell) {
	d.mu.Lock()
	defer d.mu.Unlock()

	kept := d.problems[:0]
	for _, problem := range d.problems {
		if problem.GetKey() == cell.Key && problem.GetFolder() == cell.Folder {
			continue
		}
		kept = append(kept, problem)
	}
	d.problems = kept
}
