package envgate_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func optional(key string) *resourcesv1.VariableDefinition {
	d := def(key, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	d.Required = false
	return d
}

func row(t *testing.T, m envgate.Matrix, key string) envgate.MatrixRow {
	t.Helper()
	for _, r := range m.Rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("matrix has no row for %q; rows are %+v", key, m.Rows)
	return envgate.MatrixRow{}
}

func cell(t *testing.T, r envgate.MatrixRow, folder string) envgate.MatrixCell {
	t.Helper()
	for _, c := range r.Cells {
		if c.Folder == folder {
			return c
		}
	}
	t.Fatalf("row %q has no cell for folder %q; cells are %+v", r.Key, folder, r.Cells)
	return envgate.MatrixCell{}
}

func app(t *testing.T, m envgate.Matrix, name string) envgate.AppResolution {
	t.Helper()
	for _, a := range m.Apps {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("matrix has no readout for app %q; apps are %+v", name, m.Apps)
	return envgate.AppResolution{}
}

func TestMatrix(t *testing.T) {
	t.Parallel()

	t.Run("columns are the root plus every folder declared or bound", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "api"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/admin", "/web"))

		want := []string{"", "/admin", "/web"}
		if got := g.Matrix(nil).Columns; !reflect.DeepEqual(got, want) {
			t.Errorf("columns = %q, want %q — the root first, then every folder either side declares", got, want)
		}
	})

	t.Run("an unscoped key is required at the root and an override in every folder", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		r := row(t, g.Matrix(nil), "API_URL")
		if got := cell(t, r, "").State; got != envgate.CellRequired {
			t.Errorf("API_URL at the root is %q, want %q", got, envgate.CellRequired)
		}
		if got := cell(t, r, "/web").State; got != envgate.CellOptional {
			t.Errorf("API_URL in /web is %q, want %q — a folder value is an override the root still backs", got, envgate.CellOptional)
		}
	})

	t.Run("a key with a default is never required", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		declare(t, g, optional("LOG_LEVEL"))

		if got := cell(t, row(t, g.Matrix(nil), "LOG_LEVEL"), "").State; got != envgate.CellOptional {
			t.Errorf("LOG_LEVEL at the root is %q, want %q — its schema supplies the value", got, envgate.CellOptional)
		}
	})

	t.Run("a scoped key is required in every folder it names and forbidden everywhere else", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
			{Name: "jobs", Folder: "/jobs"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/web", "/admin"))

		r := row(t, g.Matrix(nil), "POSTHOG_ID")
		for _, folder := range []string{"/web", "/admin"} {
			if got := cell(t, r, folder).State; got != envgate.CellRequired {
				t.Errorf("POSTHOG_ID in %s is %q, want %q", folder, got, envgate.CellRequired)
			}
		}
		if got := cell(t, r, "").State; got != envgate.CellForbidden {
			t.Errorf("POSTHOG_ID at the root is %q, want %q — a scoped key has no root value at all", got, envgate.CellForbidden)
		}
		if got := cell(t, r, "/jobs").State; got != envgate.CellForbidden {
			t.Errorf("POSTHOG_ID in /jobs is %q, want %q — /jobs is outside its scope", got, envgate.CellForbidden)
		}
	})

	t.Run("a forbidden cell is exactly one the write path refuses", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})
		declare(t, g,
			def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			scoped("POSTHOG_ID", "/web"),
			optional("LOG_LEVEL"),
		)

		m := g.Matrix(nil)
		checked := 0
		for _, r := range m.Rows {
			for _, c := range r.Cells {
				writable := envgate.CheckWritable(g.Definitions(), r.Key, c.Folder) == nil
				if writable == (c.State == envgate.CellForbidden) {
					t.Errorf("%s in %q is %q but the write path writable=%v", r.Key, c.Folder, c.State, writable)
				}
				checked++
			}
		}
		if checked != 9 {
			t.Fatalf("compared %d cells, want 9 — 3 keys across the root, /web and /admin", checked)
		}
	})

	t.Run("a stored value marks its cell filled", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "https://root.example")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		r := row(t, g.Matrix(nil), "API_URL")
		if !cell(t, r, "").Set {
			t.Error("the root cell is unset, want it filled — the store holds a value for it")
		}
		if cell(t, r, "/web").Set {
			t.Error("the /web cell is filled, want it unset — nothing overrides the root there")
		}
	})

	t.Run("a malformed value keeps its cell filled and carries the schema's complaint", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "not-a-url")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		report(t, g, &resourcesv1.VariableProblem{
			Key:    "API_URL",
			Kind:   resourcesv1.VariableProblem_KIND_INVALID,
			Detail: "must be a URL",
		})

		c := cell(t, row(t, g.Matrix(nil), "API_URL"), "")
		if !c.Set {
			t.Error("the cell is unset, want it filled — a malformed value is still a value")
		}
		if c.Problem != "must be a URL" {
			t.Errorf("cell problem = %q, want the schema's own message", c.Problem)
		}
	})

	t.Run("an override is named beside a cell it does not fill", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-7", "override")
		values.override("STRIPE_API_KEY", "", "pr-42", "override")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		c := cell(t, row(t, g.Matrix([]string{"pr-7", "pr-42"}), "STRIPE_API_KEY"), "")
		if c.Set {
			t.Error("the cell reports filled, want it empty — an override is not the value every environment reads")
		}
		want := []envgate.Override{
			{Environment: "pr-42", Version: 1},
			{Environment: "pr-7", Version: 1},
		}
		if !reflect.DeepEqual(c.Overrides, want) {
			t.Errorf("overrides = %+v, want %+v — a surviving override must be visible, not silent", c.Overrides, want)
		}
	})

	t.Run("marks an override whose environment is gone", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-7", "override")
		values.override("STRIPE_API_KEY", "", "staging", "override")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		c := cell(t, row(t, g.Matrix([]string{"staging"}), "STRIPE_API_KEY"), "")
		want := []envgate.Override{
			{Environment: "pr-7", Version: 1, Orphaned: true},
			{Environment: "staging", Version: 1},
		}
		if !reflect.DeepEqual(c.Overrides, want) {
			t.Errorf("overrides = %+v, want %+v", c.Overrides, want)
		}
	})

	t.Run("the environments a cell names are the caller's to keep", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "", "pr-7", "override")
		values.override("STRIPE_API_KEY", "", "pr-42", "override")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		environments := []string{"pr-7", "pr-42"}
		cell(t, row(t, g.Matrix(environments), "STRIPE_API_KEY"), "").Overrides[0].Environment = "clobbered"

		want := []envgate.Override{
			{Environment: "pr-42", Version: 1},
			{Environment: "pr-7", Version: 1},
		}
		if got := cell(t, row(t, g.Matrix(environments), "STRIPE_API_KEY"), "").Overrides; !reflect.DeepEqual(got, want) {
			t.Errorf("overrides = %+v, want %+v — one caller's edit reached the gate's own record", got, want)
		}
	})

	t.Run("an override earns its folder a column that satisfies nothing", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.override("STRIPE_API_KEY", "/worker", "pr-42", "override")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		m := g.Matrix([]string{"pr-42"})
		if want := []string{"", "/worker"}; !reflect.DeepEqual(m.Columns, want) {
			t.Fatalf("columns = %q, want %q — an override in a folder nothing else names is still a value to show", m.Columns, want)
		}

		c := cell(t, row(t, m, "STRIPE_API_KEY"), "/worker")
		if c.Set {
			t.Error("the /worker cell reports filled, want it empty — no deploy resolves a named environment's value")
		}
		if c.State != envgate.CellOptional {
			t.Errorf("the /worker cell is %q, want %q — a column drawn for an override owes nobody anything", c.State, envgate.CellOptional)
		}
		if want := []envgate.Override{{Environment: "pr-42", Version: 1}}; !reflect.DeepEqual(c.Overrides, want) {
			t.Errorf("overrides = %+v, want %+v — the column exists to carry exactly this", c.Overrides, want)
		}

		var refusal *envgate.Refusal
		if !errors.As(g.Check(), &refusal) {
			t.Fatal("Check err is not an *envgate.Refusal — the root cell is still owed")
		}
		var owed []envgate.Cell
		for _, problem := range refusal.Problems {
			owed = append(owed, envgate.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()})
		}
		if want := []envgate.Cell{{Key: "STRIPE_API_KEY"}}; !reflect.DeepEqual(owed, want) {
			t.Errorf("the deploy is refused over %+v, want %+v — a column drawn for an override must not reach the verdict", owed, want)
		}
	})

	t.Run("a cell carries the version a write against it must expect", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.setAt("API_URL", "", "https://root.example", 4)

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		r := row(t, g.Matrix(nil), "API_URL")
		if got := cell(t, r, "").Version; got != 4 {
			t.Errorf("root cell version = %d, want 4 — the page quotes it back so a stale edit is refused", got)
		}
		if got := cell(t, r, "/web").Version; got != 0 {
			t.Errorf("/web cell version = %d, want 0 — an unset cell expects no live value", got)
		}
	})

	t.Run("an app resolves only when both its hops find every required key", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "https://root.example")
		values.set("POSTHOG_ID", "/web", "ph_web")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})
		declare(t, g,
			def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			scoped("POSTHOG_ID", "/web", "/admin"),
		)

		m := g.Matrix(nil)
		if got := app(t, m, "web").Missing; len(got) != 0 {
			t.Errorf("web is missing %+v, want it to resolve — the root backs API_URL and /web holds POSTHOG_ID", got)
		}
		want := []envgate.Cell{{Key: "POSTHOG_ID", Folder: "/admin"}}
		if got := app(t, m, "admin").Missing; !reflect.DeepEqual(got, want) {
			t.Errorf("admin is missing %+v, want %+v — the cell it owes, named where it owes it", got, want)
		}
	})

	t.Run("an app resolves without a key whose schema supplies the value", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		declare(t, g, optional("LOG_LEVEL"))

		if got := app(t, g.Matrix(nil), "web").Missing; len(got) != 0 {
			t.Errorf("web is missing %+v, want nothing — the schema's default is the value", got)
		}
	})

	t.Run("an app is not blamed for a key scoped away from its folder", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "jobs", Folder: "/jobs"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/web"))

		if got := app(t, g.Matrix(nil), "jobs").Missing; len(got) != 0 {
			t.Errorf("jobs is missing %+v, want nothing — POSTHOG_ID is out of its scope, not absent from it", got)
		}
	})

	t.Run("an unbound app owes the root cell it could not read", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		want := []envgate.Cell{{Key: "STRIPE_API_KEY"}}
		if got := app(t, g.Matrix(nil), "api").Missing; !reflect.DeepEqual(got, want) {
			t.Errorf("api is missing %+v, want %+v", got, want)
		}
	})

	t.Run("rows carry the class and scope that decide their cells", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
		declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET), scoped("POSTHOG_ID", "/web"))

		m := g.Matrix(nil)
		if got := row(t, m, "STRIPE_API_KEY").Class; got != "secret" {
			t.Errorf("STRIPE_API_KEY class = %q, want %q", got, "secret")
		}
		if got := row(t, m, "POSTHOG_ID").Scope; !reflect.DeepEqual(got, []string{"/web"}) {
			t.Errorf("POSTHOG_ID scope = %q, want [/web]", got)
		}
	})
}

func TestForget(t *testing.T) {
	t.Parallel()

	t.Run("dropping a cell drops what discovery said about the value it held", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "not-a-url")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		report(t, g, &resourcesv1.VariableProblem{
			Key:    "API_URL",
			Kind:   resourcesv1.VariableProblem_KIND_INVALID,
			Detail: "must be a URL",
		})

		g.Forget(envgate.Cell{Key: "API_URL"})

		if got := cell(t, row(t, g.Matrix(nil), "API_URL"), "").Problem; got != "" {
			t.Errorf("cell still complains %q, want it cleared — the value it described has been replaced", got)
		}
		if err := g.Check(); err != nil {
			t.Errorf("Check = %v, want nil — the only problem was about a value that is gone", err)
		}
	})
}
