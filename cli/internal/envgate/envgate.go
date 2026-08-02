// Package envgate is the discovery gate for variables: it serves the
// declaration half of the resource service during a discovery run, hands the
// declaring process the cells the store already holds, and refuses the deploy
// when that process reports a cell it cannot run with.
//
// The verdict has two halves. Whether a value satisfies its schema is only
// knowable where the schema is: schemas are live objects in the application's
// own language and cannot be serialised, so the declaring process reports
// that. Whether a required value is there at all is not a schema question and
// this package answers it itself, from the declarations and the store — so a
// declaring process that reports nothing, because it is older or because it
// threw after declaring, cannot let a missing value through to runtime.
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

// Cell addresses one stored value, across every environment that reads it. A
// named-environment override is a value the run that is that environment
// resolves from the same cell, never a second cell of its own — a key that
// varied by environment would let one environment's value stand in for the
// class-wide value every other reads.
type Cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
}

// Address is one row's coordinates: the cell, and the named environment holding
// it — empty for the class-wide value every environment falls back to. It is
// what Reveal reads at and what the UI writes, deletes and reads history at, so
// none of those has an address of its own to keep in step.
//
// Environment stays here rather than on Cell because a cell is a map key
// throughout the verdict, and a key that varied by environment would let an
// override stand in for the class-wide value nothing else can supply.
type Address struct {
	Cell        Cell
	Environment string
}

// Stored is one row the store holds: where it is, and the version a write
// against it must expect.
type Stored struct {
	Address
	Version int64
}

// Override is one named environment's own value for a cell: the environment
// holding it, the version a write against it must expect, and whether that
// environment still exists.
type Override struct {
	Environment string `json:"environment"`
	Version     int64  `json:"version"`
	Orphaned    bool   `json:"orphaned,omitempty"`
}

// Orphaned reports an override the provider no longer enumerates an
// environment for. The value survives a preview's removal — losing it would
// destroy a long-lived branch's configuration the moment its environment was
// rebuilt — so what is left is a row nothing will ever read, and every surface
// that lists overrides says so from this one rule rather than its own.
//
// environments is what exists. The class-wide value is never orphaned: it is
// the value an environment falls back to, not one bound to any.
func Orphaned(environments []string, environment string) bool {
	return environment != "" && !slices.Contains(environments, environment)
}

// heldCells is a set of cells with values, at the version each is held at. One
// map answers both questions a verdict asks — whether a cell is filled, and
// which value filled it — so the two can never drift apart.
type heldCells map[Cell]int64

func (h heldCells) has(cell Cell) bool {
	_, ok := h[cell]
	return ok
}

// classWideCells is the store's class-wide set: what every environment reads
// where none of its own holds a value. It is what the matrix draws, because the
// matrix presents every environment's values at once rather than standing in
// any one of them.
//
// Callers hold g.mu.
func (g *Gate) classWideCells() heldCells {
	cells := make(heldCells, len(g.cells))
	for _, row := range g.cells {
		cells[row.Cell] = row.Version
	}
	return cells
}

// resolvedCells is what this run actually reads: the class-wide set, overlaid
// with the overrides the run's own environment holds. It is what the verdict
// and the deploy's resolution are both formed from, so a cell an override
// answers is never one the run is called short of — the gate would otherwise
// refuse an environment for a value that environment is the only holder of, and
// refuse it for a value the live path serves.
//
// A run bound to no environment sees the class-wide set and nothing else, which
// is every production deploy: an override answers only where the run is the
// environment holding it.
//
// Callers hold g.mu.
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

// ownOverride is the version this run's own environment holds for a cell, and
// whether it holds one at all.
//
// Callers hold g.mu.
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

// Values is the store as the gate needs it. The CLI has no cloud SDK
// dependency, so both operations are answered by the provider binary.
type Values interface {
	// List enumerates every value the project holds, class-wide and
	// environment-scoped alike, as presence without plaintext.
	List(ctx context.Context) ([]Stored, error)

	// Reveal decrypts the named rows in one query and answers with the
	// plaintext of those that hold a value; a row that holds none is absent
	// from the result. It is called only for classes whose values a build
	// legitimately holds — a live-class cell is never named.
	//
	// The answer is keyed by cell rather than by row because a run resolves
	// one environment: each cell is asked for at exactly one address, and
	// which address that was is this call's business rather than its caller's.
	Reveal(ctx context.Context, rows []Address) (map[Cell]string, error)
}

// App is one application and the variable folder it binds. An empty Folder is
// an app that reads the project root, which is every app until one needs to
// diverge.
type App struct {
	Name   string
	Folder string
}

// Scope is the run the gate rules for: which apps read a cell and which
// substrate a fixing command has to address — what a refusal has to name to be
// actionable — and which named environment the run is deploying.
//
// Environment is empty for production, which has a single environment, and for
// any run not bound to one: an override answers only where the run is the
// environment holding it.
type Scope struct {
	Apps        []App
	Preview     bool
	Environment string
}

// Gate accumulates a discovery run's declarations and its verdict.
type Gate struct {
	values Values
	scope  Scope

	mu sync.Mutex
	// cells is the class-wide set and nothing else; overrides is the rest, held
	// apart so a named-environment value can answer for the one environment
	// holding it without ever counting as the value every other environment
	// resolves. What each read of them means is classWideCells and
	// resolvedCells, which is where the two are put back together.
	cells       []Stored
	overrides   map[Cell][]Override
	definitions []*resourcesv1.VariableDefinition
	problems    []*resourcesv1.VariableProblem

	// plaintext is what this run has already decrypted, so a cell any two of
	// its declarations and resolutions reach for is fetched once. A cell that
	// holds no value is recorded as found: false rather than left out, so
	// asking again costs nothing either. Prefetch clears it: the cells it
	// re-reads are the answer to a write that just landed.
	plaintext map[Cell]revealed
}

type revealed struct {
	value string
	found bool
}

func New(values Values, scope Scope) *Gate {
	return &Gate{values: values, scope: scope}
}

// Prefetch reads the project's current cells in one query, before discovery
// runs, so a store that cannot be reached fails the deploy up front rather
// than in the middle of a declaration.
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

// reveal decrypts every named cell the run has not already read, in one call,
// and answers from what it holds. It is the only path to plaintext in this
// package: a caller names the cells its class permits, and how many round trips
// that costs is decided here rather than at each call site.
//
// Each cell is read at the address this run resolves it from: the override the
// run's own environment holds, where there is one, and the class-wide value
// everywhere else. That is the same rule the runtime applies to a live value,
// applied here because a baked value's read time is the deploy.
//
// The lock is held across the read. Declarations arrive concurrently — a
// discovery run starts every defineEnv's call before it awaits any of them — so
// releasing it would let two of them fetch the same cell at once, which is the
// per-cell round trip this exists to remove.
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

// resolvedEnvironment is where one cell's value comes from for this run: the
// run's own environment when that environment holds an override, and class-wide
// otherwise.
//
// Callers hold g.mu.
func (g *Gate) resolvedEnvironment(cell Cell) string {
	if _, ok := g.ownOverride(cell); ok {
		return g.scope.Environment
	}
	return ""
}

// DeclareEnv records one defineEnv call's definitions and answers with the
// cells the store holds for exactly those keys. A live-class cell is answered
// with presence and no plaintext, which is what keeps a CI build host from
// ever holding a live secret.
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

// cellsOf is every cell this run holds for one key, in folder order so one
// declaration is answered the same way twice.
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

// ReportEnvProblems records the declaring process's verdict.
func (g *Gate) ReportEnvProblems(_ context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.problems = append(g.problems, req.GetProblems()...)
	return &resourcesv1.ReportEnvProblemsResponse{}, nil
}

// Definitions returns every variable this discovery run declared.
func (g *Gate) Definitions() []*resourcesv1.VariableDefinition {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*resourcesv1.VariableDefinition(nil), g.definitions...)
}

// Check is the gate itself: it returns a *Refusal when any app cannot resolve
// a required cell, or when discovery reported one it cannot run with, and nil
// otherwise.
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

// unresolved is the gate's own half of the verdict: every required cell an app
// cannot resolve, from the same two-hop rule the deploy resolves values by, so
// what the gate refuses on and what the build would have run with are one
// answer. A cell the declaring process already complained about is left to it —
// its message is the more specific of the two.
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

// readers is who a verdict is formed for. A project that configures no apps
// still deploys one, bound to the project root, so an empty scope stands for
// that app rather than for no reader at all — otherwise a gate with nothing
// configured would have nothing to be short of.
func readers(apps []App) []App {
	if len(apps) == 0 {
		return []App{{}}
	}
	return append([]App(nil), apps...)
}

// Refusal is a deploy stopped before anything was built. It carries the
// problems as well as the message so a recovery path can prefill exactly the
// cells that are missing.
type Refusal struct {
	Problems []*resourcesv1.VariableProblem
	Scope    Scope
}

func (r *Refusal) Error() string {
	return r.Owed() + "\nSet the values above, then run this command again."
}

// Owed is the refusal without its closing advice: the headline and the cells
// that stopped the run, and nothing about what to do next. A caller that is
// about to offer its own way out in this same run — the recovery that opens the
// variables UI and waits — prints this and supplies its own next step, because
// "run this command again" is wrong advice for a command that has not given up.
//
// It carries key names and folders only. A value never appears in a refusal.
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

// describeAll names every cell one read covered, sorted so the same failure
// reads the same way twice. A batched read fails whole, so what a user is short
// of is every cell it asked for — a message naming none of them leaves nothing
// to act on.
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

// readBy names the apps a failing cell actually belongs to. A root cell is the
// fallback for every app; a folder cell is read only by the app bound there.
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
