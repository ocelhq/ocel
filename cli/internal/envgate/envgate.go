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

// Cell addresses one stored value. Only the class-wide cells matter to the
// gate: a named-environment override is a value, not a requirement.
type Cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
}

// Stored is one row the store holds: which cell it addresses, which named
// environment it belongs to if any, and the version a write against it must
// expect. Only a class-wide row's version is kept — nothing here writes to a
// named environment, so an override is reduced to the name of the environment
// holding it. Environment stays here rather than on Cell because a cell is a
// map key throughout the verdict, and a key that varied by environment would
// let an override stand in for the class-wide value nothing else can supply.
type Stored struct {
	Cell        Cell
	Environment string
	Version     int64
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

// heldCells is every cell the store holds, at the version it holds it at. One
// map answers both questions a verdict asks — whether a cell is filled, and
// which value filled it — so the two can never drift apart.
type heldCells map[Cell]int64

func (h heldCells) has(cell Cell) bool {
	_, ok := h[cell]
	return ok
}

// heldCells reads the store rows the gate has, under the lock its caller
// already holds.
func (g *Gate) heldCells() heldCells {
	held := make(heldCells, len(g.cells))
	for _, row := range g.cells {
		held[row.Cell] = row.Version
	}
	return held
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
	Reveal(ctx context.Context, rows []Read) (map[Cell]string, error)
}

// Read is one row to decrypt: a cell, and the named environment whose override
// answers for it when this run resolves one. The environment lives here rather
// than on Cell because a cell is a map key throughout the verdict, and a key
// that varied by environment would let an override stand in for the class-wide
// value nothing else can supply.
type Read struct {
	Cell        Cell
	Environment string
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
	// cells is the class-wide set and nothing else: it is what the gate's
	// verdict, the matrix and every answer to a declaration are read from.
	// overrides is the rest, held apart so a named-environment value can be
	// shown without ever counting as the value a deploy resolves.
	cells       []Stored
	overrides   map[Cell][]string
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
	overrides := map[Cell][]string{}
	for _, row := range stored {
		if row.Environment == "" {
			cells = append(cells, row)
			continue
		}
		overrides[row.Cell] = append(overrides[row.Cell], row.Environment)
	}
	for _, environments := range overrides {
		sort.Strings(environments)
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

	var wanted []Read
	seen := map[Cell]bool{}
	for _, cell := range cells {
		if _, held := g.plaintext[cell]; held || seen[cell] {
			continue
		}
		seen[cell] = true
		wanted = append(wanted, Read{Cell: cell, Environment: g.resolvedEnvironment(cell)})
	}

	if len(wanted) > 0 {
		found, err := g.values.Reveal(ctx, wanted)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", describeAll(wanted), err)
		}
		if g.plaintext == nil {
			g.plaintext = map[Cell]revealed{}
		}
		for _, read := range wanted {
			value, ok := found[read.Cell]
			g.plaintext[read.Cell] = revealed{value: value, found: ok}
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
// otherwise. A run bound to no environment always resolves class-wide, which is
// every production deploy.
//
// Callers hold g.mu.
func (g *Gate) resolvedEnvironment(cell Cell) string {
	if g.scope.Environment == "" || !slices.Contains(g.overrides[cell], g.scope.Environment) {
		return ""
	}
	return g.scope.Environment
}

// DeclareEnv records one defineEnv call's definitions and answers with the
// cells the store holds for exactly those keys. A live-class cell is answered
// with presence and no plaintext, which is what keeps a CI build host from
// ever holding a live secret.
func (g *Gate) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	g.mu.Lock()
	stored := append([]Stored(nil), g.cells...)
	g.definitions = append(g.definitions, req.GetDefinitions()...)
	g.mu.Unlock()

	var wanted []Cell
	for _, definition := range req.GetDefinitions() {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			continue
		}
		for _, row := range stored {
			if row.Cell.Key == definition.GetKey() {
				wanted = append(wanted, row.Cell)
			}
		}
	}
	plaintext, err := g.reveal(ctx, wanted)
	if err != nil {
		return nil, err
	}

	var cells []*resourcesv1.VariableCell
	for _, definition := range req.GetDefinitions() {
		live := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		for _, row := range stored {
			cell := row.Cell
			if cell.Key != definition.GetKey() {
				continue
			}
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
	held := g.heldCells()
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
func describeAll(rows []Read) string {
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
