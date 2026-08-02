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
	held      map[envgate.Read]string
	revealed  []envgate.Read
}

func newFakeValues() *fakeValues {
	return &fakeValues{cells: map[envgate.Cell]string{}, versions: map[envgate.Cell]int64{}, held: map[envgate.Read]string{}}
}

func (v *fakeValues) set(key, folder, value string) {
	v.cells[envgate.Cell{Key: key, Folder: folder}] = value
	v.held[envgate.Read{Cell: envgate.Cell{Key: key, Folder: folder}}] = value
}

func (v *fakeValues) setAt(key, folder, value string, version int64) {
	v.set(key, folder, value)
	v.versions[envgate.Cell{Key: key, Folder: folder}] = version
}

// override is the value one named environment holds for a cell, beside whatever
// the class-wide row holds.
func (v *fakeValues) override(key, folder, environment, value string) {
	cell := envgate.Cell{Key: key, Folder: folder}
	v.overrides = append(v.overrides, envgate.Stored{Cell: cell, Environment: environment, Version: 1})
	v.held[envgate.Read{Cell: cell, Environment: environment}] = value
}

func (v *fakeValues) List(context.Context) ([]envgate.Stored, error) {
	out := make([]envgate.Stored, 0, len(v.cells)+len(v.overrides))
	for c := range v.cells {
		out = append(out, envgate.Stored{Cell: c, Version: v.versions[c]})
	}
	return append(out, v.overrides...), nil
}

func (v *fakeValues) Reveal(_ context.Context, rows []envgate.Read) (map[envgate.Cell]string, error) {
	v.revealed = append(v.revealed, rows...)
	found := map[envgate.Cell]string{}
	for _, row := range rows {
		if value, ok := v.held[row]; ok {
			found[row.Cell] = value
		}
	}
	return found, nil
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

	for _, row := range values.revealed {
		if row.Cell.Key == "STRIPE_API_KEY" {
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
	values.override("STRIPE_API_KEY", "", "pr-42", "override")

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

// An override answers for the environment holding it and for nothing else, so a
// run bound to no environment must never see one. Each class leaks differently,
// so each is asked the question it can answer: a live class answers presence
// without reading, so a leaked override surfaces as a cell; a readable class
// reads before answering, so it surfaces as a decrypt onto the build host.
func TestGate_AnOverrideIsNeverAnsweredToARunThatIsNotThatEnvironment(t *testing.T) {
	t.Run("a live class names no cell for it", func(t *testing.T) {
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-42", "override")

		g := prefetched(t, values)
		resp := declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		if len(resp.GetCells()) != 0 {
			t.Errorf("cells = %+v, want none — only pr-42 holds a value and no deploy reads it", resp.GetCells())
		}
	})

	t.Run("a readable class never decrypts it", func(t *testing.T) {
		values := newFakeValues()
		values.override("ANALYTICS_ID", "", "pr-42", "override")

		g := prefetched(t, values)
		declare(t, g, def("ANALYTICS_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if len(values.revealed) != 0 {
			t.Errorf("revealed = %+v, want nothing decrypted for a cell the class-wide set does not hold", values.revealed)
		}
	})
}

// The binding rule at the other read time. A live value resolves its override
// in the sandbox; a baked one is read here, at the deploy, so the same rule has
// to hold here or an override would only ever work for one class.
func TestGate_TheRunsOwnEnvironmentResolvesItsOverrideAndEveryOtherResolvesClassWide(t *testing.T) {
	for name, tc := range map[string]struct {
		environment string
		want        string
	}{
		"the environment holding the override": {environment: "pr-42", want: "override"},
		"another environment":                  {environment: "pr-7", want: "class-wide"},
		"a run bound to no environment":        {want: "class-wide"},
	} {
		t.Run(name, func(t *testing.T) {
			values := newFakeValues()
			values.set("ANALYTICS_ID", "", "class-wide")
			values.override("ANALYTICS_ID", "", "pr-42", "override")

			g := prefetched(t, values, envgate.Scope{Preview: true, Environment: tc.environment})
			resp := declare(t, g, def("ANALYTICS_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

			cells := resp.GetCells()
			if len(cells) != 1 || cells[0].GetValue() != tc.want {
				t.Fatalf("cells = %+v, want the one ANALYTICS_ID cell holding %q", cells, tc.want)
			}
			if len(values.revealed) != 1 {
				t.Errorf("revealed = %+v, want one read: a cell is resolved from one address, not probed at two", values.revealed)
			}
		})
	}
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

// TestRefusal_OwedNamesTheCellsWithoutTheAdviceToRerun: a caller that is about
// to offer its own way out in this same run prints Owed, so Owed must carry
// every cell and none of the advice.
func TestRefusal_OwedNamesTheCellsWithoutTheAdviceToRerun(t *testing.T) {
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		},
		Scope: envgate.Scope{Apps: []envgate.App{{Name: "web"}}},
	}

	owed := refusal.Owed()
	if !strings.Contains(owed, "STRIPE_API_KEY") {
		t.Errorf("Owed() = %q, want it to name the cell that stopped the run", owed)
	}
	if strings.Contains(owed, "run this command again") {
		t.Errorf("Owed() = %q, want no advice to re-run a command that has not given up", owed)
	}
	if !strings.Contains(refusal.Error(), "run this command again") {
		t.Errorf("Error() = %q, want the hard refusal to still say what to do next", refusal.Error())
	}
}
