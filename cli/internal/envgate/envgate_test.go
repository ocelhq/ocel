package envgate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// fakeValues is the store as the gate sees it, recording every reveal so a
// test can assert on what plaintext the build host was allowed to hold.
type fakeValues struct {
	cells     map[envgate.Cell]string
	versions  map[envgate.Cell]int64
	overrides []envgate.Stored
	revealed  []envgate.Cell
}

func newFakeValues() *fakeValues {
	return &fakeValues{cells: map[envgate.Cell]string{}, versions: map[envgate.Cell]int64{}}
}

func (v *fakeValues) set(key, folder, value string) {
	v.cells[envgate.Cell{Key: key, Folder: folder}] = value
}

func (v *fakeValues) setAt(key, folder, value string, version int64) {
	v.set(key, folder, value)
	v.versions[envgate.Cell{Key: key, Folder: folder}] = version
}

// override is what `ocel env set --environment` writes.
func (v *fakeValues) override(key, folder, environment string) {
	v.overrides = append(v.overrides, envgate.Stored{
		Cell:        envgate.Cell{Key: key, Folder: folder},
		Environment: environment,
		Version:     1,
	})
}

func (v *fakeValues) List(context.Context) ([]envgate.Stored, error) {
	out := make([]envgate.Stored, 0, len(v.cells)+len(v.overrides))
	for c := range v.cells {
		out = append(out, envgate.Stored{Cell: c, Version: v.versions[c]})
	}
	return append(out, v.overrides...), nil
}

func (v *fakeValues) Reveal(_ context.Context, cell envgate.Cell) (string, bool, error) {
	v.revealed = append(v.revealed, cell)
	value, ok := v.cells[cell]
	return value, ok, nil
}

func declare(t *testing.T, g *envgate.Gate, definitions ...*resourcesv1.VariableDefinition) *resourcesv1.DeclareEnvResponse {
	t.Helper()
	resp, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	return resp
}

func report(t *testing.T, g *envgate.Gate, problems ...*resourcesv1.VariableProblem) {
	t.Helper()
	if _, err := g.ReportEnvProblems(context.Background(), &resourcesv1.ReportEnvProblemsRequest{Problems: problems}); err != nil {
		t.Fatalf("ReportEnvProblems: %v", err)
	}
}

func first(scopes []envgate.Scope) envgate.Scope {
	if len(scopes) == 0 {
		return envgate.Scope{}
	}
	return scopes[0]
}

func def(key string, class resourcesv1.VariableClass) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: class, Required: true}
}

func prefetched(t *testing.T, values envgate.Values, scope ...envgate.Scope) *envgate.Gate {
	t.Helper()
	g := envgate.New(values, first(scope))
	if err := g.Prefetch(context.Background()); err != nil {
		t.Fatalf("Prefetch: %v", err)
	}
	return g
}

func TestGate_MissingCellNamesTheKeyAndTheCommandThatFixesIt(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}, {Name: "web"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE))
	report(t, g, &resourcesv1.VariableProblem{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY <VALUE>", "api", "web"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal = %q, want it to contain %q", msg, want)
		}
	}
}

func TestGate_InvalidCellIsRefusedLikeAMissingOneAndNamesWhatFailed(t *testing.T) {
	values := newFakeValues()
	values.set("WEBHOOK_URL", "", "not-a-url")
	g := prefetched(t, values)
	declare(t, g, def("WEBHOOK_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{
		Key:    "WEBHOOK_URL",
		Kind:   resourcesv1.VariableProblem_KIND_INVALID,
		Detail: "invalid url",
	})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal")
	}
	for _, want := range []string{"WEBHOOK_URL", "invalid url", "ocel env set WEBHOOK_URL <VALUE>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestGate_ProblemInAFolderIsFixedByAFolderScopedCommand(t *testing.T) {
	values := newFakeValues()
	values.set("POSTHOG_ID", "/checkout", "")
	g := prefetched(t, values)
	declare(t, g, def("POSTHOG_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{Key: "POSTHOG_ID", Folder: "/checkout", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "too small"})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "ocel env set POSTHOG_ID <VALUE> --folder /checkout") {
		t.Errorf("refusal = %q, want the folder-scoped fixing command", err.Error())
	}
}

func TestGate_PreviewRefusalNamesThePreviewSubstrate(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Preview: true})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "ocel env set STRIPE_API_KEY <VALUE> --preview") {
		t.Errorf("refusal = %q, want the preview-scoped fixing command", err.Error())
	}
}

func TestGate_LiveClassCellIsNeverRevealedButIsStillReportedPresent(t *testing.T) {
	values := newFakeValues()
	values.set("STRIPE_API_KEY", "", "sk_live_do_not_leak")
	values.set("ANALYTICS_ID", "", "ph_public")
	g := prefetched(t, values)

	resp := declare(t, g,
		def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		def("ANALYTICS_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
	)

	for _, cell := range values.revealed {
		if cell.Key == "STRIPE_API_KEY" {
			t.Fatalf("revealed = %+v, want a live-class value never decrypted for the build host", values.revealed)
		}
	}

	byKey := map[string]*resourcesv1.VariableCell{}
	for _, c := range resp.GetCells() {
		byKey[c.GetKey()] = c
	}
	live, ok := byKey["STRIPE_API_KEY"]
	if !ok {
		t.Fatal("cells has no STRIPE_API_KEY, want the live cell reported present")
	}
	if live.GetValue() != "" {
		t.Errorf("live cell value = %q, want presence only", live.GetValue())
	}
	if got := byKey["ANALYTICS_ID"].GetValue(); got != "ph_public" {
		t.Errorf("baked cell value = %q, want the plaintext the schema is checked against", got)
	}
}

func TestGate_ReportsOnlyCellsOfKeysThatWereDeclared(t *testing.T) {
	values := newFakeValues()
	values.set("DECLARED", "", "yes")
	values.set("UNDECLARED", "", "no")
	g := prefetched(t, values)

	resp := declare(t, g, def("DECLARED", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	for _, c := range resp.GetCells() {
		if c.GetKey() == "UNDECLARED" {
			t.Errorf("cells = %+v, want only the declared key's cells", resp.GetCells())
		}
	}
}

func TestGate_NothingReportedLetsTheDeployProceed(t *testing.T) {
	values := newFakeValues()
	values.set("STRIPE_API_KEY", "", "sk_test")
	g := prefetched(t, values)
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	if err := g.Check(); err != nil {
		t.Fatalf("Check err = %v, want a complete matrix to proceed", err)
	}
}

// TestGate_RequiredCellWithNoValueRefusesThoughDiscoveryReportedNothing proves
// the refusal does not depend on the declaring process saying so. An older SDK,
// a defineEnv that throws after declaring, or any bug in the reporting half
// leaves the gate with nothing but its own knowledge — and presence is not a
// schema question, so the gate already has the answer.
func TestGate_RequiredCellWithNoValueRefusesThoughDiscoveryReportedNothing(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE))

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a required value nothing holds to refuse without being told")
	}
	for _, want := range []string{"STRIPE_API_KEY", "no value is set", "ocel env set STRIPE_API_KEY <VALUE>", "api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to contain %q", err.Error(), want)
		}
	}
}

// TestGate_UnreportedMissingFolderCellNamesTheFolderThatOwesIt proves the
// gate's own verdict follows the same two-hop resolution the deploy does: only
// the app bound to the empty folder is short, and the refusal addresses that
// folder rather than the project root.
func TestGate_UnreportedMissingFolderCellNamesTheFolderThatOwesIt(t *testing.T) {
	values := newFakeValues()
	values.set("POSTHOG_ID", "/web", "ph_web")
	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "admin", Folder: "/admin"},
	}})
	declare(t, g, &resourcesv1.VariableDefinition{
		Key:      "POSTHOG_ID",
		Class:    resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Required: true,
		Folders:  []string{"/web", "/admin"},
	})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want the folder holding no value to refuse")
	}
	if !strings.Contains(err.Error(), "ocel env set POSTHOG_ID <VALUE> --folder /admin") {
		t.Errorf("refusal = %q, want it to address the folder that owes the value", err.Error())
	}
	if strings.Contains(err.Error(), "--folder /web") {
		t.Errorf("refusal = %q, want the folder that already holds a value left out", err.Error())
	}
}

// TestGate_UnsetOptionalValueIsNotARefusal is the counterweight: the gate
// forming its own verdict must not turn every empty cell into a stop.
func TestGate_UnsetOptionalValueIsNotARefusal(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, &resourcesv1.VariableDefinition{
		Key:   "ANALYTICS_ID",
		Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
	})

	if err := g.Check(); err != nil {
		t.Fatalf("Check err = %v, want an optional value nothing set to proceed", err)
	}
}

// TestGate_ReportedProblemIsNotDoubledByTheGatesOwnVerdict proves the two
// halves of the verdict meet as one list: a cell both sides call missing is
// named once.
func TestGate_ReportedProblemIsNotDoubledByTheGatesOwnVerdict(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING})

	var refusal *envgate.Refusal
	if !errors.As(g.Check(), &refusal) {
		t.Fatal("Check err is not an *envgate.Refusal")
	}
	if len(refusal.Problems) != 1 {
		t.Errorf("refusal carries %d problems, want the one cell named once: %+v", len(refusal.Problems), refusal.Problems)
	}
}

// TestGate_SchemaInvalidCellIsStillTheDeclaringProcessesToReport proves the
// gate's own verdict does not displace the SDK's: a value that is present but
// wrong is only knowable where the schema lives, and it must still refuse.
func TestGate_SchemaInvalidCellIsStillTheDeclaringProcessesToReport(t *testing.T) {
	values := newFakeValues()
	values.set("WEBHOOK_URL", "", "not-a-url")
	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, def("WEBHOOK_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{Key: "WEBHOOK_URL", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "invalid url"})

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want the schema complaint to refuse")
	}
	if !strings.Contains(err.Error(), "invalid url") {
		t.Errorf("refusal = %q, want the schema's own complaint", err.Error())
	}
}

func TestGate_AnOverrideOnlyKeyDoesNotSatisfyTheDeployGate(t *testing.T) {
	values := newFakeValues()
	values.override("STRIPE_API_KEY", "", "pr-42")

	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal — pr-42 holds a value the deploy never resolves, so the class-wide cell is still empty")
	}
	for _, want := range []string{"STRIPE_API_KEY", "no value is set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to contain %q", err.Error(), want)
		}
	}
}

// Each class leaks differently, so each is asked the question it can answer: a
// live class answers presence without reading, so a leaked override surfaces
// as a cell; a readable class reads before answering, so it surfaces as a
// decrypt onto the build host.
func TestGate_AnOverrideIsNeverAnsweredToTheDeclaringProcess(t *testing.T) {
	t.Run("a live class names no cell for it", func(t *testing.T) {
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-42")

		g := prefetched(t, values)
		resp := declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		if len(resp.GetCells()) != 0 {
			t.Errorf("cells = %+v, want none — only pr-42 holds a value and no deploy reads it", resp.GetCells())
		}
	})

	t.Run("a readable class never decrypts it", func(t *testing.T) {
		values := newFakeValues()
		values.override("ANALYTICS_ID", "", "pr-42")

		g := prefetched(t, values)
		declare(t, g, def("ANALYTICS_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if len(values.revealed) != 0 {
			t.Errorf("revealed = %+v, want nothing decrypted for a cell the class-wide set does not hold", values.revealed)
		}
	})
}

func TestGate_RefusalCarriesTheProblemsSoARecoveryPathCanPrefillThem(t *testing.T) {
	g := prefetched(t, newFakeValues())
	declare(t, g, def("A_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g,
		&resourcesv1.VariableProblem{Key: "A_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		&resourcesv1.VariableProblem{Key: "A_KEY", Folder: "/web", Kind: resourcesv1.VariableProblem_KIND_MISSING},
	)

	var refusal *envgate.Refusal
	if !errors.As(g.Check(), &refusal) {
		t.Fatal("Check err is not an *envgate.Refusal")
	}
	if len(refusal.Problems) != 2 {
		t.Errorf("refusal carries %d problems, want both cells", len(refusal.Problems))
	}
}
