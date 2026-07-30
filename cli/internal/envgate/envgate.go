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

// Values is the store as the gate needs it. The CLI has no cloud SDK
// dependency, so both operations are answered by the provider binary.
type Values interface {
	// List enumerates every class-wide cell the project holds, as presence
	// without plaintext.
	List(ctx context.Context) ([]Cell, error)

	// Reveal decrypts one cell. It is called only for classes whose values a
	// build legitimately holds; a live-class value is never revealed.
	Reveal(ctx context.Context, cell Cell) (string, bool, error)
}

// App is one application and the variable folder it binds. An empty Folder is
// an app that reads the project root, which is every app until one needs to
// diverge.
type App struct {
	Name   string
	Folder string
}

// Scope is what a refusal has to name to be actionable: which apps read the
// cell, and which substrate the fixing command has to address.
type Scope struct {
	Apps    []App
	Preview bool
}

// Gate accumulates a discovery run's declarations and its verdict.
type Gate struct {
	values Values
	scope  Scope

	mu          sync.Mutex
	cells       []Cell
	definitions []*resourcesv1.VariableDefinition
	problems    []*resourcesv1.VariableProblem
}

func New(values Values, scope Scope) *Gate {
	return &Gate{values: values, scope: scope}
}

// Prefetch reads the project's current cells in one query, before discovery
// runs, so a store that cannot be reached fails the deploy up front rather
// than in the middle of a declaration.
func (g *Gate) Prefetch(ctx context.Context) error {
	cells, err := g.values.List(ctx)
	if err != nil {
		return fmt.Errorf("read this project's variable values: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.cells = cells
	return nil
}

// DeclareEnv records one defineEnv call's definitions and answers with the
// cells the store holds for exactly those keys. A live-class cell is answered
// with presence and no plaintext, which is what keeps a CI build host from
// ever holding a live secret.
func (g *Gate) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	g.mu.Lock()
	stored := append([]Cell(nil), g.cells...)
	g.definitions = append(g.definitions, req.GetDefinitions()...)
	g.mu.Unlock()

	var cells []*resourcesv1.VariableCell
	for _, definition := range req.GetDefinitions() {
		live := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		for _, cell := range stored {
			if cell.Key != definition.GetKey() {
				continue
			}
			var value string
			if !live {
				revealed, found, err := g.values.Reveal(ctx, cell)
				if err != nil {
					return nil, fmt.Errorf("read %s: %w", describe(cell), err)
				}
				if !found {
					continue
				}
				value = revealed
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
	held := make(map[Cell]bool, len(g.cells))
	for _, cell := range g.cells {
		held[cell] = true
	}
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
func unresolved(definitions []*resourcesv1.VariableDefinition, apps []App, held map[Cell]bool, reported []*resourcesv1.VariableProblem) []*resourcesv1.VariableProblem {
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
	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — nothing has been built.\n", plural(len(r.Problems)))
	for _, problem := range r.Problems {
		cell := Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s\n    %s\n    fix: %s\n",
			describe(cell)+readBy(r.Scope.Apps, cell.Folder), why(problem), fixCommand(cell, r.Scope))
	}
	b.WriteString("\nSet the values above, then run this command again.")
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
