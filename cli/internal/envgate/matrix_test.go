package envgate_test

import (
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

func TestMatrix_ColumnsAreTheRootPlusEveryFolderDeclaredOrBound(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "api"},
	}})
	declare(t, g, scoped("POSTHOG_ID", "/admin", "/web"))

	want := []string{"", "/admin", "/web"}
	if got := g.Matrix().Columns; !reflect.DeepEqual(got, want) {
		t.Errorf("columns = %q, want %q — the root first, then every folder either side declares", got, want)
	}
}

func TestMatrix_AnUnscopedKeyIsRequiredAtTheRootAndAnOverrideInEveryFolder(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
	declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	r := row(t, g.Matrix(), "API_URL")
	if got := cell(t, r, "").State; got != envgate.CellRequired {
		t.Errorf("API_URL at the root is %q, want %q", got, envgate.CellRequired)
	}
	if got := cell(t, r, "/web").State; got != envgate.CellOptional {
		t.Errorf("API_URL in /web is %q, want %q — a folder value is an override the root still backs", got, envgate.CellOptional)
	}
}

func TestMatrix_AKeyWithADefaultIsNeverRequired(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	declare(t, g, optional("LOG_LEVEL"))

	if got := cell(t, row(t, g.Matrix(), "LOG_LEVEL"), "").State; got != envgate.CellOptional {
		t.Errorf("LOG_LEVEL at the root is %q, want %q — its schema supplies the value", got, envgate.CellOptional)
	}
}

func TestMatrix_AScopedKeyIsRequiredInEveryFolderItNamesAndForbiddenEverywhereElse(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "admin", Folder: "/admin"},
		{Name: "jobs", Folder: "/jobs"},
	}})
	declare(t, g, scoped("POSTHOG_ID", "/web", "/admin"))

	r := row(t, g.Matrix(), "POSTHOG_ID")
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
}

// The matrix draws the cells a user may type into, and CheckWritable decides
// what the write path accepts. If those two ever disagree the UI either offers
// a cell that cannot be saved or hides one that could, so they are asserted
// against each other rather than separately.
func TestMatrix_AForbiddenCellIsExactlyOneTheWritePathRefuses(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "admin", Folder: "/admin"},
	}})
	declare(t, g,
		def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		scoped("POSTHOG_ID", "/web"),
		optional("LOG_LEVEL"),
	)

	m := g.Matrix()
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
}

func TestMatrix_AStoredValueMarksItsCellFilled(t *testing.T) {
	values := newFakeValues()
	values.set("API_URL", "", "https://root.example")

	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
	declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	r := row(t, g.Matrix(), "API_URL")
	if !cell(t, r, "").Set {
		t.Error("the root cell is unset, want it filled — the store holds a value for it")
	}
	if cell(t, r, "/web").Set {
		t.Error("the /web cell is filled, want it unset — nothing overrides the root there")
	}
}

func TestMatrix_AMalformedValueKeepsItsCellFilledAndCarriesTheSchemasComplaint(t *testing.T) {
	values := newFakeValues()
	values.set("API_URL", "", "not-a-url")

	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
	report(t, g, &resourcesv1.VariableProblem{
		Key:    "API_URL",
		Kind:   resourcesv1.VariableProblem_KIND_INVALID,
		Detail: "must be a URL",
	})

	c := cell(t, row(t, g.Matrix(), "API_URL"), "")
	if !c.Set {
		t.Error("the cell is unset, want it filled — a malformed value is still a value")
	}
	if c.Problem != "must be a URL" {
		t.Errorf("cell problem = %q, want the schema's own message", c.Problem)
	}
}

func TestMatrix_AnAppResolvesOnlyWhenBothItsHopsFindEveryRequiredKey(t *testing.T) {
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

	m := g.Matrix()
	if got := app(t, m, "web").Missing; len(got) != 0 {
		t.Errorf("web is missing %+v, want it to resolve — the root backs API_URL and /web holds POSTHOG_ID", got)
	}
	want := []envgate.Cell{{Key: "POSTHOG_ID", Folder: "/admin"}}
	if got := app(t, m, "admin").Missing; !reflect.DeepEqual(got, want) {
		t.Errorf("admin is missing %+v, want %+v — the cell it owes, named where it owes it", got, want)
	}
}

func TestMatrix_AnAppResolvesWithoutAKeyWhoseSchemaSuppliesTheValue(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	declare(t, g, optional("LOG_LEVEL"))

	if got := app(t, g.Matrix(), "web").Missing; len(got) != 0 {
		t.Errorf("web is missing %+v, want nothing — the schema's default is the value", got)
	}
}

func TestMatrix_AnAppIsNotBlamedForAKeyScopedAwayFromItsFolder(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "jobs", Folder: "/jobs"},
	}})
	declare(t, g, scoped("POSTHOG_ID", "/web"))

	if got := app(t, g.Matrix(), "jobs").Missing; len(got) != 0 {
		t.Errorf("jobs is missing %+v, want nothing — POSTHOG_ID is out of its scope, not absent from it", got)
	}
}

func TestMatrix_AnUnboundAppOwesTheRootCellItCouldNotRead(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

	want := []envgate.Cell{{Key: "STRIPE_API_KEY"}}
	if got := app(t, g.Matrix(), "api").Missing; !reflect.DeepEqual(got, want) {
		t.Errorf("api is missing %+v, want %+v", got, want)
	}
}

func TestMatrix_RowsCarryTheClassAndScopeThatDecideTheirCells(t *testing.T) {
	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
	declare(t, g, def("STRIPE_API_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET), scoped("POSTHOG_ID", "/web"))

	m := g.Matrix()
	if got := row(t, m, "STRIPE_API_KEY").Class; got != "secret" {
		t.Errorf("STRIPE_API_KEY class = %q, want %q", got, "secret")
	}
	if got := row(t, m, "POSTHOG_ID").Scope; !reflect.DeepEqual(got, []string{"/web"}) {
		t.Errorf("POSTHOG_ID scope = %q, want [/web]", got)
	}
}

func TestMatrix_ForgettingACellDropsWhatDiscoverySaidAboutTheValueItHeld(t *testing.T) {
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

	if got := cell(t, row(t, g.Matrix(), "API_URL"), "").Problem; got != "" {
		t.Errorf("cell still complains %q, want it cleared — the value it described has been replaced", got)
	}
	if err := g.Check(); err != nil {
		t.Errorf("Check = %v, want nil — the only problem was about a value that is gone", err)
	}
}
