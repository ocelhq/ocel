package variables_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func scoped(key string, folders ...string) *resourcesv1.VariableDefinition {
	d := def(key, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	d.Folders = folders
	return d
}

func resolve(t *testing.T, g *variables.Declarations, app string) map[string]variables.ResolvedValue {
	t.Helper()
	resolved, err := g.Resolve(context.Background(), app)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", app, err)
	}
	return resolved
}

func TestAnAppResolvesEachKeyFromItsOwnFolderOrTheRoot(t *testing.T) {
	t.Parallel()

	t.Run("two apps bound to different folders resolve different values", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("POSTHOG_ID", "/web", "ph_web")
		values.set("POSTHOG_ID", "/admin", "ph_admin")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/web", "/admin"))

		if got := resolve(t, g, "web")["POSTHOG_ID"].Value; got != "ph_web" {
			t.Errorf("web resolves POSTHOG_ID = %q, want %q", got, "ph_web")
		}
		if got := resolve(t, g, "admin")["POSTHOG_ID"].Value; got != "ph_admin" {
			t.Errorf("admin resolves POSTHOG_ID = %q, want %q", got, "ph_admin")
		}
	})

	t.Run("an app with no binding resolves from root", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "https://root.example")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}})
		declare(t, g, def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		got := resolve(t, g, "api")["API_URL"]
		if got.Value != "https://root.example" || got.Folder != "" {
			t.Errorf("api resolves API_URL = %+v, want the root value", got)
		}
	})

	t.Run("a bound app falls back to root for an unscoped key", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("API_URL", "", "https://root.example")
		values.set("LOG_LEVEL", "", "info")
		values.set("LOG_LEVEL", "/web", "debug")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "web", Folder: "/web"}}})
		declare(t, g,
			def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		)

		resolved := resolve(t, g, "web")
		if got := resolved["API_URL"]; got.Value != "https://root.example" || got.Folder != "" {
			t.Errorf("API_URL = %+v, want the root value the folder does not override", got)
		}
		if got := resolved["LOG_LEVEL"]; got.Value != "debug" || got.Folder != "/web" {
			t.Errorf("LOG_LEVEL = %+v, want the bound folder to win over root", got)
		}
	})

	t.Run("a nested folder resolves through root, not its parent", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("LOG_LEVEL", "", "info")
		values.set("LOG_LEVEL", "/web", "debug")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "admin", Folder: "/web/admin"}}})
		declare(t, g, def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if got := resolve(t, g, "admin")["LOG_LEVEL"]; got.Value != "info" || got.Folder != "" {
			t.Errorf("LOG_LEVEL = %+v, want root — /web is not a lookup for /web/admin", got)
		}
	})

	t.Run("a scoped key is absent for an app outside its scope", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("POSTHOG_ID", "/web", "ph_web")
		values.set("POSTHOG_ID", "/admin", "ph_leftover")
		values.set("POSTHOG_ID", "", "ph_root")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
			{Name: "api"},
		}})
		declare(t, g, scoped("POSTHOG_ID", "/web"))

		for _, app := range []string{"admin", "api"} {
			if got, ok := resolve(t, g, app)["POSTHOG_ID"]; ok {
				t.Errorf("%s resolved POSTHOG_ID = %+v, want a key outside an app's scope never delivered to it", app, got)
			}
		}
	})

	t.Run("refuses an app that is not in the project", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), variables.Scope{Apps: []variables.App{{Name: "web", Folder: "/web"}}})

		if _, err := g.Resolve(context.Background(), "ghost"); err == nil {
			t.Fatal("Resolve(ghost) err = nil, want an unknown app refused rather than silently resolved from root")
		}
	})

	t.Run("a live value is resolved as an address without its plaintext", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("SESSION_SECRET", "", "sk_live_do_not_leak")

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}})
		declare(t, g, def("SESSION_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		got, ok := resolve(t, g, "api")["SESSION_SECRET"]
		if !ok {
			t.Fatal("SESSION_SECRET absent, want the cell a live value is fetched from at runtime")
		}
		if got.Value != "" {
			t.Errorf("SESSION_SECRET value = %q, want a live value never pulled onto the build host", got.Value)
		}
	})

	t.Run("reports the version of the cell each key resolved from", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.setAt("KEY", "", "root", 3)
		values.setAt("KEY", "/api", "api", 7)

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{
			{Name: "api", Folder: "/api"},
			{Name: "web"},
		}})
		declare(t, g, def("KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if got := resolve(t, g, "api")["KEY"]; got.Version != 7 {
			t.Errorf("api resolves KEY = %+v, want the /api cell's version 7", got)
		}
		if got := resolve(t, g, "web")["KEY"]; got.Version != 3 {
			t.Errorf("web resolves KEY = %+v, want the root cell's version 3", got)
		}
	})

	t.Run("a live key reports its cell's version even with no value", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.setAt("SESSION_SECRET", "", "sk_live_do_not_leak", 5)

		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}})
		declare(t, g, def("SESSION_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		got := resolve(t, g, "api")["SESSION_SECRET"]
		if got.Value != "" || got.Version != 5 {
			t.Errorf("SESSION_SECRET = %+v, want no plaintext but the cell's version 5", got)
		}
	})
}
