package envgate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type fakeValues struct {
	cells     map[envgate.Cell]string
	versions  map[envgate.Cell]int64
	overrides []envgate.Stored
	held      map[envgate.Address]string
	revealed  []envgate.Address
}

func newFakeValues() *fakeValues {
	return &fakeValues{cells: map[envgate.Cell]string{}, versions: map[envgate.Cell]int64{}, held: map[envgate.Address]string{}}
}

func (v *fakeValues) set(key, folder, value string) {
	v.cells[envgate.Cell{Key: key, Folder: folder}] = value
	v.held[envgate.Address{Cell: envgate.Cell{Key: key, Folder: folder}}] = value
}

func (v *fakeValues) setAt(key, folder, value string, version int64) {
	v.set(key, folder, value)
	v.versions[envgate.Cell{Key: key, Folder: folder}] = version
}

func (v *fakeValues) override(key, folder, environment, value string) {
	cell := envgate.Cell{Key: key, Folder: folder}
	v.overrides = append(v.overrides, envgate.Stored{Address: envgate.Address{Cell: cell, Environment: environment}, Version: 1})
	v.held[envgate.Address{Cell: cell, Environment: environment}] = value
}

func (v *fakeValues) List(context.Context) ([]envgate.Stored, error) {
	out := make([]envgate.Stored, 0, len(v.cells)+len(v.overrides))
	for c := range v.cells {
		out = append(out, envgate.Stored{Address: envgate.Address{Cell: c}, Version: v.versions[c]})
	}
	return append(out, v.overrides...), nil
}

func (v *fakeValues) Reveal(_ context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
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

func TestCheck(t *testing.T) {
	t.Parallel()

	t.Run("a missing cell names the key and the command that fixes it", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("an invalid cell is refused like a missing one and names what failed", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a problem in a folder is fixed by a folder-scoped command", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a preview refusal names the preview bootstrap", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a refusal names only the apps that read the failing cell", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("POSTHOG_ID", "/admin", "ph_admin")
		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
			{Name: "api"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/web", "/admin"))
		report(t, g, &resourcesv1.VariableProblem{
			Key: "POSTHOG_ID", Folder: "/web", Kind: resourcesv1.VariableProblem_KIND_MISSING,
		})

		message := g.Check().Error()
		if !strings.Contains(message, "web") {
			t.Errorf("refusal = %q, want it to name the app bound to /web", message)
		}
		if strings.Contains(message, "admin") || strings.Contains(message, "api") {
			t.Errorf("refusal = %q, want only the apps that read /web named", message)
		}
	})

	t.Run("nothing reported lets the deploy proceed", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("STRIPE_API_KEY", "", "sk_test")
		g := prefetched(t, values)
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if err := g.Check(); err != nil {
			t.Fatalf("Check err = %v, want a complete matrix to proceed", err)
		}
	})

	t.Run("a required cell with no value refuses though discovery reported nothing", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("an unreported missing folder cell names the folder that owes it", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("an unset optional value is not a refusal", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, &resourcesv1.VariableDefinition{
			Key:   "ANALYTICS_ID",
			Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		})

		if err := g.Check(); err != nil {
			t.Fatalf("Check err = %v, want an optional value nothing set to proceed", err)
		}
	})

	t.Run("a reported problem is not doubled by the gate's own verdict", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a schema-invalid cell is still the declaring process's to report", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("an override-only key does not satisfy the deploy gate", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-42", "override")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		err := g.Check()
		if err == nil {
			t.Fatal("Check err = nil, want a refusal — pr-42 holds a value the deploy never resolves, so the cell every environment reads is still empty")
		}
		for _, want := range []string{"STRIPE_API_KEY", "no value is set"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal = %q, want it to contain %q", err.Error(), want)
			}
		}
	})

	t.Run("an override satisfies the gate for the run that is that environment", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "staging", "sk_staging")

		g := prefetched(t, values, envgate.Scope{
			Apps:        []envgate.App{{Name: "api"}},
			Preview:     true,
			Environment: "staging",
		})
		resp := declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if err := g.Check(); err != nil {
			t.Fatalf("Check err = %v, want staging's own override to satisfy the gate for staging", err)
		}
		cells := resp.GetCells()
		if len(cells) != 1 || cells[0].GetValue() != "sk_staging" {
			t.Errorf("cells = %+v, want the one cell holding staging's own value", cells)
		}
	})

	t.Run("a refusal carries the problems so a recovery path can prefill them", func(t *testing.T) {
		t.Parallel()
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
	})
}

func TestDeclareEnv(t *testing.T) {
	t.Parallel()

	t.Run("a live-class cell is never revealed but is still reported present", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("refuses a variable declared as derived", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues())

		_, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
			Definitions: []*resourcesv1.VariableDefinition{
				def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_DERIVED),
			},
		})
		if err == nil {
			t.Fatal("DeclareEnv accepted a derived declaration; ocel writes and prunes that class, so the user's would be overwritten")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("err = %v, want the key it refused named", err)
		}
	})

	t.Run("reports only cells of keys that were declared", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("an override is never answered to a run that is not that environment, and a live class names no cell for it", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-42", "override")

		g := prefetched(t, values)
		resp := declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		if len(resp.GetCells()) != 0 {
			t.Errorf("cells = %+v, want none — only pr-42 holds a value and no deploy reads it", resp.GetCells())
		}
	})

	t.Run("an override is never answered to a run that is not that environment, and a readable class never decrypts it", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("ANALYTICS_ID", "", "pr-42", "override")

		g := prefetched(t, values)
		declare(t, g, def("ANALYTICS_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if len(values.revealed) != 0 {
			t.Errorf("revealed = %+v, want nothing decrypted for a cell the base set does not hold", values.revealed)
		}
	})

	for _, tc := range []struct {
		name        string
		environment string
		want        string
	}{
		{"the run's own environment resolves its override", "pr-42", "override"},
		{"another environment resolves the base value", "pr-7", "base"},
		{"a run bound to no environment resolves the base value", "", "base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			values := newFakeValues()
			values.set("ANALYTICS_ID", "", "base")
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

func TestRefusal(t *testing.T) {
	t.Parallel()

	t.Run("Owed names the cells without the advice to rerun", func(t *testing.T) {
		t.Parallel()
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
	})
}
