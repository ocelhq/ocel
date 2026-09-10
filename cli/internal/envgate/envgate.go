package envgate

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
}

type Address struct {
	Cell        Cell
	Environment string
}

type Stored struct {
	Address
	Version   int64
	Reference *Reference
}

type Reference struct {
	Slug   string `json:"slug"`
	Folder string `json:"folder"`
	Key    string `json:"key"`
}

type Override struct {
	Environment string     `json:"environment"`
	Version     int64      `json:"version"`
	Orphaned    bool       `json:"orphaned,omitempty"`
	Reference   *Reference `json:"reference,omitempty"`
}

func Orphaned(environments []string, environment string) bool {
	return environment != "" && !slices.Contains(environments, environment)
}

type Values interface {
	List(ctx context.Context) ([]Stored, error)

	Reveal(ctx context.Context, rows []Address) (map[Cell]string, error)
}

type App struct {
	Name   string
	Folder string
}

type Scope struct {
	Apps        []App
	Preview     bool
	Environment string
	Browser     bool
}

type Gate struct {
	values Values
	scope  Scope

	mu          sync.Mutex
	cells       []Stored
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

func New(values Values, scope Scope) *Gate {
	return &Gate{values: values, scope: scope}
}

func (g *Gate) Prefetch(ctx context.Context) error {
	stored, err := g.values.List(ctx)
	if err != nil {
		return fmt.Errorf("read this project's variable values: %w", err)
	}

	var cells []Stored
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

	g.mu.Lock()
	defer g.mu.Unlock()
	g.cells = cells
	g.overrides = overrides
	g.references = references
	g.plaintext = nil
	return nil
}

func (g *Gate) reveal(ctx context.Context, cells []Cell) (map[Cell]revealed, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var wanted []Address
	seen := map[Cell]bool{}
	for _, cell := range cells {
		if _, held := g.plaintext[cell]; held || seen[cell] {
			continue
		}
		seen[cell] = true
		wanted = append(wanted, Address{Cell: cell, Environment: g.resolvedEnvironment(cell)})
	}

	if len(wanted) > 0 {
		found, err := g.values.Reveal(ctx, wanted)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", describeAll(wanted), err)
		}
		if g.plaintext == nil {
			g.plaintext = map[Cell]revealed{}
		}
		for _, at := range wanted {
			value, ok := found[at.Cell]
			g.plaintext[at.Cell] = revealed{value: value, found: ok}
		}
	}

	out := make(map[Cell]revealed, len(cells))
	for _, cell := range cells {
		out[cell] = g.plaintext[cell]
	}
	return out, nil
}

func (g *Gate) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	groups := map[string]bool{}
	for _, group := range req.GetGroups() {
		if group.GetKey() == "" {
			return nil, fmt.Errorf("an environment group needs a key")
		}
		if groups[group.GetKey()] {
			return nil, fmt.Errorf("environment group %s is declared twice", group.GetKey())
		}
		groups[group.GetKey()] = true
	}
	for _, definition := range req.GetDefinitions() {
		if definition.GetGroup() != "" && !groups[definition.GetGroup()] {
			return nil, fmt.Errorf("%s belongs to environment group %s, but that group is not declared", definition.GetKey(), definition.GetGroup())
		}
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_DERIVED {
			return nil, fmt.Errorf("%s is declared as derived, a class ocel writes for the resources an app binds and prunes on its own; declare it as plain, sensitive or secret", definition.GetKey())
		}
		if providerkit.OcelWritten(definition.GetKey()) {
			return nil, fmt.Errorf("%s is written by ocel for every app, from the hostname this deploy serves it on, so a declared one would be overwritten before anything read it; read it from `ocel/env` as `deployment.url` instead of declaring it", definition.GetKey())
		}
	}
	for _, group := range req.GetGroups() {
		if !slices.ContainsFunc(req.GetDefinitions(), func(definition *resourcesv1.VariableDefinition) bool {
			return definition.GetGroup() == group.GetKey()
		}) {
			return nil, fmt.Errorf("environment group %s has no members", group.GetKey())
		}
	}

	held, err := g.claim(req)
	if err != nil {
		return nil, err
	}

	var wanted []Cell
	for _, definition := range req.GetDefinitions() {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			continue
		}
		wanted = append(wanted, cellsOf(held, definition.GetKey())...)
	}
	plaintext, err := g.reveal(ctx, wanted)
	if err != nil {
		return nil, err
	}

	var cells []*resourcesv1.VariableCell
	for _, definition := range req.GetDefinitions() {
		live := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		for _, cell := range cellsOf(held, definition.GetKey()) {
			var value string
			if !live {
				if !plaintext[cell].found {
					continue
				}
				value = plaintext[cell].value
			}
			cells = append(cells, &resourcesv1.VariableCell{
				Key:    cell.Key,
				Folder: cell.Folder,
				Value:  value,
			})
		}
	}

	return &resourcesv1.DeclareEnvResponse{Cells: cells}, nil
}

func (g *Gate) claim(req *resourcesv1.DeclareEnvRequest) (heldCells, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, group := range req.GetGroups() {
		if slices.ContainsFunc(g.groups, func(held *resourcesv1.GroupDefinition) bool { return held.GetKey() == group.GetKey() }) {
			return nil, fmt.Errorf("environment group %s is declared twice", group.GetKey())
		}
	}
	held := g.resolvedCells()
	g.definitions = append(g.definitions, req.GetDefinitions()...)
	g.groups = append(g.groups, req.GetGroups()...)
	return held, nil
}

func (g *Gate) Groups() []*resourcesv1.GroupDefinition {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.groups)
}

func (g *Gate) ReportEnvProblems(_ context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.problems = append(g.problems, req.GetProblems()...)
	return &resourcesv1.ReportEnvProblemsResponse{}, nil
}

func (g *Gate) Definitions() []*resourcesv1.VariableDefinition {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.definitions)
}

func (g *Gate) Check() error {
	g.mu.Lock()
	problems := slices.Clone(g.problems)
	definitions := slices.Clone(g.definitions)
	groups := slices.Clone(g.groups)
	apps := readers(g.scope.Apps)
	held := g.resolvedCells()
	g.mu.Unlock()

	problems = append(problems, unresolved(definitions, groups, apps, held, problems)...)
	if len(problems) == 0 {
		return nil
	}
	slices.SortStableFunc(problems, func(a, b *resourcesv1.VariableProblem) int {
		if c := cmp.Compare(declaredAt(definitions, a.GetKey()), declaredAt(definitions, b.GetKey())); c != 0 {
			return c
		}
		if c := cmp.Compare(a.GetKey(), b.GetKey()); c != 0 {
			return c
		}
		return cmp.Compare(a.GetFolder(), b.GetFolder())
	})
	return &Refusal{Problems: problems, Definitions: definitions, Groups: groups, Scope: g.scope}
}

func declaredAt(definitions []*resourcesv1.VariableDefinition, key string) int {
	for i, definition := range definitions {
		if definition.GetKey() == key {
			return i
		}
	}
	return len(definitions)
}

func unresolved(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, apps []App, held heldCells, reported []*resourcesv1.VariableProblem) []*resourcesv1.VariableProblem {
	named := make(map[Cell]bool, len(reported))
	for _, problem := range reported {
		named[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = true
	}

	var problems []*resourcesv1.VariableProblem
	for _, app := range apps {
		for _, cell := range missing(definitions, groups, app.Folder, held) {
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
