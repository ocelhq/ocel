package variables

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	connect "connectrpc.com/connect"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Declarations struct {
	values Values
	scope  Scope

	mu          sync.Mutex
	cells       []ValueMetadata
	overrides   map[Cell][]Override
	references  map[Cell]*Reference
	definitions []*resourcesv1.VariableDefinition
	groups      []*resourcesv1.GroupDefinition
	problems    []*resourcesv1.VariableProblem

	plaintext map[Cell]revealed
}

type revealed struct {
	value string
	found bool
}

func NewDeclarations(values Values, scope Scope) *Declarations {
	return &Declarations{values: values, scope: scope}
}

func (d *Declarations) Prefetch(ctx context.Context) error {
	stored, err := d.values.List(ctx)
	if err != nil {
		return fmt.Errorf("read this project's variable values: %w", err)
	}

	var cells []ValueMetadata
	overrides := map[Cell][]Override{}
	references := map[Cell]*Reference{}
	for _, row := range stored {
		if row.Environment == "" {
			cells = append(cells, row)
			if row.Reference != nil {
				references[row.Cell] = row.Reference
			}
			continue
		}
		overrides[row.Cell] = append(overrides[row.Cell], Override{Environment: row.Environment, Version: row.Version, Reference: row.Reference})
	}
	for _, forCell := range overrides {
		slices.SortFunc(forCell, func(a, b Override) int { return cmp.Compare(a.Environment, b.Environment) })
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cells = cells
	d.overrides = overrides
	d.references = references
	d.plaintext = nil
	return nil
}

func (d *Declarations) reveal(ctx context.Context, cells []Cell) (map[Cell]revealed, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var wanted []Coordinate
	seen := map[Cell]bool{}
	for _, cell := range cells {
		if _, ok := d.plaintext[cell]; ok || seen[cell] {
			continue
		}
		seen[cell] = true
		wanted = append(wanted, Coordinate{Cell: cell, Environment: d.resolvedEnvironment(cell)})
	}

	if len(wanted) > 0 {
		found, err := d.values.Reveal(ctx, wanted)
		if err != nil {
			return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("read %s: %w", describeAll(wanted), err))
		}
		if d.plaintext == nil {
			d.plaintext = map[Cell]revealed{}
		}
		for _, at := range wanted {
			value, ok := found[at]
			d.plaintext[at.Cell] = revealed{value: value, found: ok}
		}
	}

	out := make(map[Cell]revealed, len(cells))
	for _, cell := range cells {
		out[cell] = d.plaintext[cell]
	}
	return out, nil
}

func (d *Declarations) claim(req *resourcesv1.DeclareEnvRequest) (presentCells, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, group := range req.GetGroups() {
		if slices.ContainsFunc(d.groups, func(existing *resourcesv1.GroupDefinition) bool { return existing.GetKey() == group.GetKey() }) {
			return nil, fmt.Errorf("environment group %s is declared twice", group.GetKey())
		}
	}
	present := d.resolvedCells()
	d.definitions = append(d.definitions, req.GetDefinitions()...)
	d.groups = append(d.groups, req.GetGroups()...)
	return present, nil
}

func (d *Declarations) Scope() Scope {
	return d.scope
}

func (d *Declarations) Groups() []*resourcesv1.GroupDefinition {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.groups)
}

func (d *Declarations) Definitions() []*resourcesv1.VariableDefinition {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.definitions)
}

func (d *Declarations) RefuseIncomplete() error {
	d.mu.Lock()
	problems := slices.Clone(d.problems)
	definitions := slices.Clone(d.definitions)
	groups := slices.Clone(d.groups)
	apps := readers(d.scope.Apps)
	present := d.resolvedCells()
	d.mu.Unlock()

	bound := d.scope.bindingDefinitions()
	problems = append(problems, unresolved(definitions, groups, apps, present, problems)...)
	problems = append(problems, unsetBindingVariables(bound, present)...)
	if len(problems) == 0 {
		return nil
	}
	definitions = append(definitions, d.scope.impliedDefinitions()...)
	groups = append(groups, d.scope.impliedGroups()...)
	slices.SortStableFunc(problems, func(a, b *resourcesv1.VariableProblem) int {
		if c := cmp.Compare(declaredAt(definitions, a.GetKey()), declaredAt(definitions, b.GetKey())); c != 0 {
			return c
		}
		if c := cmp.Compare(a.GetKey(), b.GetKey()); c != 0 {
			return c
		}
		return cmp.Compare(a.GetFolder(), b.GetFolder())
	})
	return &MissingError{Problems: problems, Definitions: definitions, Groups: groups, Scope: d.scope}
}

func (d *Declarations) RefuseCredentials(problems []*resourcesv1.VariableProblem) *MissingError {
	return &MissingError{
		Problems:    problems,
		Definitions: d.Declared(),
		Groups:      append(d.Groups(), d.scope.impliedGroups()...),
		Scope:       d.scope,
	}
}

func declaredAt(definitions []*resourcesv1.VariableDefinition, key string) int {
	for i, definition := range definitions {
		if definition.GetKey() == key {
			return i
		}
	}
	return len(definitions)
}

func unresolved(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, apps []App, present presentCells, reported []*resourcesv1.VariableProblem) []*resourcesv1.VariableProblem {
	named := make(map[Cell]bool, len(reported))
	for _, problem := range reported {
		named[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = true
	}

	var problems []*resourcesv1.VariableProblem
	for _, app := range apps {
		for _, cell := range missing(definitions, groups, app.Folder, present) {
			if named[cell] {
				continue
			}
			named[cell] = true
			problems = append(problems, &resourcesv1.VariableProblem{
				Key:    cell.Key,
				Folder: cell.Folder,
				Kind:   resourcesv1.VariableProblem_KIND_MISSING,
			})
		}
	}
	return problems
}

func readers(apps []App) []App {
	if len(apps) == 0 {
		return []App{{}}
	}
	return slices.Clone(apps)
}
