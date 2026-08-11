package envgate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func scoped(key string, folders ...string) *resourcesv1.VariableDefinition {
	d := def(key, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)
	d.Folders = folders
	return d
}

func resolve(t *testing.T, g *envgate.Gate, app string) map[string]envgate.Resolved {
	t.Helper()
	resolved, err := g.Resolve(context.Background(), app)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", app, err)
	}
	return resolved
}

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("two apps bound to different folders resolve different values", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("POSTHOG_ID", "/web", "ph_web")
		values.set("POSTHOG_ID", "/admin", "ph_admin")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
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

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
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

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
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

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "admin", Folder: "/web/admin"}}})
		declare(t, g, def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if got := resolve(t, g, "admin")["LOG_LEVEL"]; got.Value != "info" || got.Folder != "" {
			t.Errorf("LOG_LEVEL = %+v, want root — /web is not a hop for /web/admin", got)
		}
	})

	t.Run("a scoped key is absent for an app outside its scope", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("POSTHOG_ID", "/web", "ph_web")
		values.set("POSTHOG_ID", "/admin", "ph_leftover")
		values.set("POSTHOG_ID", "", "ph_root")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
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
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})

		if _, err := g.Resolve(context.Background(), "ghost"); err == nil {
			t.Fatal("Resolve(ghost) err = nil, want an unknown app refused rather than silently resolved from root")
		}
	})

	t.Run("a live value is resolved as an address without its plaintext", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("SESSION_SECRET", "", "sk_live_do_not_leak")

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("SESSION_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		got, ok := resolve(t, g, "api")["SESSION_SECRET"]
		if !ok {
			t.Fatal("SESSION_SECRET absent, want the cell a live value is fetched from at runtime")
		}
		if got.Value != "" {
			t.Errorf("SESSION_SECRET value = %q, want a live value never pulled onto the build host", got.Value)
		}
	})

	t.Run("carries the version of the cell each key resolved from", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.setAt("KEY", "", "root", 3)
		values.setAt("KEY", "/api", "api", 7)

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{
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

	t.Run("a live key carries its cell's version even with no value", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.setAt("SESSION_SECRET", "", "sk_live_do_not_leak", 5)

		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "api"}}})
		declare(t, g, def("SESSION_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		got := resolve(t, g, "api")["SESSION_SECRET"]
		if got.Value != "" || got.Version != 5 {
			t.Errorf("SESSION_SECRET = %+v, want no plaintext but the cell's version 5", got)
		}
	})
}

func TestLint(t *testing.T) {
	t.Parallel()

	t.Run("a folder no app binds is a warning", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.Lint(
			[]*resourcesv1.VariableDefinition{scoped("POSTHOG_ID", "/web", "/admin")},
			[]envgate.App{{Name: "api"}},
			"ocel.config.ts",
		)
		if err != nil {
			t.Fatalf("Lint err = %v, want a scope no app reads to be a warning, not a failure", err)
		}
		joined := strings.Join(warnings, "\n")
		for _, want := range []string{"POSTHOG_ID", "/web", "/admin"} {
			if !strings.Contains(joined, want) {
				t.Errorf("warnings = %q, want %q named", joined, want)
			}
		}
	})

	t.Run("a half-completed rename is an error naming both files", func(t *testing.T) {
		t.Parallel()
		definition := scoped("POSTHOG_ID", "/web", "/admin")
		definition.Source = "apps/web/env.ts"

		_, err := envgate.Lint(
			[]*resourcesv1.VariableDefinition{definition},
			[]envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/administration"}},
			"ocel.config.ts",
		)
		if err == nil {
			t.Fatal("Lint err = nil, want a partly-bound scope refused as a half-finished rename")
		}
		for _, want := range []string{"POSTHOG_ID", "/admin", "apps/web/env.ts", "ocel.config.ts"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q named", err, want)
			}
		}
	})

	t.Run("a fully bound scope is silent", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.Lint(
			[]*resourcesv1.VariableDefinition{scoped("POSTHOG_ID", "/web", "/admin")},
			[]envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
			"ocel.config.ts",
		)
		if err != nil || len(warnings) != 0 {
			t.Errorf("Lint = %q, %v; want nothing said about a scope every app covers", warnings, err)
		}
	})

	t.Run("rejects a malformed declared folder", func(t *testing.T) {
		t.Parallel()
		_, err := envgate.Lint(
			[]*resourcesv1.VariableDefinition{scoped("POSTHOG_ID", "web#1")},
			[]envgate.App{{Name: "web", Folder: "/web"}},
			"ocel.config.ts",
		)
		if err == nil || !strings.Contains(err.Error(), "web#1") {
			t.Errorf("Lint err = %v, want the malformed folder named", err)
		}
	})
}
