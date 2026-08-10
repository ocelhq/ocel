package envgate

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
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
	Version int64
}

type Override struct {
	Environment string `json:"environment"`
	Version     int64  `json:"version"`
	Orphaned    bool   `json:"orphaned,omitempty"`
}

func Orphaned(environments []string, environment string) bool {
	return environment != "" && !slices.Contains(environments, environment)
}

type heldCells map[Cell]int64

func (h heldCells) has(cell Cell) bool {
	_, ok := h[cell]
	return ok
}

func (g *Gate) classWideCells() heldCells {
	cells := make(heldCells, len(g.cells))
	for _, row := range g.cells {
		cells[row.Cell] = row.Version
	}
	return cells
}

func (g *Gate) resolvedCells() heldCells {
	cells := g.classWideCells()
	if g.scope.Environment == "" {
		return cells
	}
	for cell := range g.overrides {
		if version, ok := g.ownOverride(cell); ok {
			cells[cell] = version
		}
	}
	return cells
}

func (g *Gate) ownOverride(cell Cell) (int64, bool) {
	if g.scope.Environment == "" {
		return 0, false
	}
	for _, override := range g.overrides[cell] {
		if override.Environment == g.scope.Environment {
			return override.Version, true
		}
	}
	return 0, false
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
}

type Gate struct {
	values Values
	scope  Scope

	mu          sync.Mutex
	cells       []Stored
	overrides   map[Cell][]Override
	definitions []*resourcesv1.VariableDefinition
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
	for _, row := range stored {
		if row.Environment == "" {
			cells = append(cells, row)
			continue
		}
		overrides[row.Cell] = append(overrides[row.Cell], Override{Environment: row.Environment, Version: row.Version})
	}
	for _, forCell := range overrides {
		sort.Slice(forCell, func(i, j int) bool { return forCell[i].Environment < forCell[j].Environment })
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.cells = cells
	g.overrides = overrides
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

func (g *Gate) resolvedEnvironment(cell Cell) string {
	if _, ok := g.ownOverride(cell); ok {
		return g.scope.Environment
	}
	return ""
}

func (g *Gate) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	g.mu.Lock()
	held := g.resolvedCells()
	g.definitions = append(g.definitions, req.GetDefinitions()...)
	g.mu.Unlock()

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

func cellsOf(held heldCells, key string) []Cell {
	var out []Cell
	for cell := range held {
		if cell.Key == key {
			out = append(out, cell)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
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
	return append([]*resourcesv1.VariableDefinition(nil), g.definitions...)
}

func (g *Gate) Check() error {
	g.mu.Lock()
	problems := append([]*resourcesv1.VariableProblem(nil), g.problems...)
	definitions := append([]*resourcesv1.VariableDefinition(nil), g.definitions...)
	apps := readers(g.scope.Apps)
	held := g.resolvedCells()
	g.mu.Unlock()

	problems = append(problems, unresolved(definitions, apps, held, problems)...)
	if len(problems) == 0 {
		return nil
	}
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].GetKey() != problems[j].GetKey() {
			return problems[i].GetKey() < problems[j].GetKey()
		}
		return problems[i].GetFolder() < problems[j].GetFolder()
	})
	return &Refusal{Problems: problems, Scope: g.scope}
}

func unresolved(definitions []*resourcesv1.VariableDefinition, apps []App, held heldCells, reported []*resourcesv1.VariableProblem) []*resourcesv1.VariableProblem {
	named := make(map[Cell]bool, len(reported))
	for _, problem := range reported {
		named[Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}] = true
	}

	var problems []*resourcesv1.VariableProblem
	for _, app := range apps {
		for _, cell := range missing(definitions, app.Folder, held) {
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
	return append([]App(nil), apps...)
}

type Refusal struct {
	Problems []*resourcesv1.VariableProblem
	Scope    Scope
}

func (r *Refusal) Error() string {
	return r.Owed() + "\nSet the values above, then run this command again."
}

func (r *Refusal) Owed() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — nothing has been built.\n", plural(len(r.Problems)))
	for _, problem := range r.Problems {
		cell := Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s\n    %s\n    fix: %s\n",
			describe(cell)+readBy(r.Scope.Apps, cell.Folder), why(problem), fixCommand(cell, r.Scope))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return "1 variable is"
	}
	return fmt.Sprintf("%d variables are", n)
}

func why(problem *resourcesv1.VariableProblem) string {
	if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
		return "set, but it does not satisfy its schema: " + problem.GetDetail()
	}
	return "no value is set"
}

func describeAll(rows []Address) string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, describe(row.Cell))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func describe(cell Cell) string {
	if cell.Folder == "" {
		return cell.Key + " (project root)"
	}
	return cell.Key + " (" + cell.Folder + ")"
}

func readBy(apps []App, folder string) string {
	var names []string
	for _, app := range apps {
		if folder == "" || app.Folder == folder {
			names = append(names, app.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return ", read by " + strings.Join(names, ", ")
}

func fixCommand(cell Cell, scope Scope) string {
	cmd := fmt.Sprintf("ocel env set %s <VALUE>", cell.Key)
	if cell.Folder != "" {
		cmd += " --folder " + cell.Folder
	}
	if scope.Preview {
		cmd += " --preview"
	}
	return cmd
}
