package attribution

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func fixtureRoot(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolve fixture %q: %v", name, err)
	}
	return root
}

func stackAt(root, file string) string {
	abs := filepath.Join(root, filepath.FromSlash(file))
	return fmt.Sprintf("Error\n    at declare (%s:3:15)\n    at ModuleJob.run (node:internal/modules/esm/module_job:271:25)", abs)
}

func monorepoDeclarations(root string) []Declaration {
	return []Declaration{
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "main-db", Stack: stackAt(root, "shared/db.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "analytics-db", Stack: stackAt(root, "shared/analytics.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "audit-db", Stack: stackAt(root, "shared/audit.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "tenant-db", Stack: stackAt(root, "shared/tenant.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_BUCKET, ID: "uploads", Stack: stackAt(root, "shared/files.ts")},
	}
}

func monorepoApps() []App {
	return []App{
		{Name: "api", Path: "apps/api"},
		{Name: "worker", Path: "apps/worker"},
	}
}

func edgeStrings(usages []Usage) []string {
	out := make([]string, 0, len(usages))
	for _, u := range usages {
		out = append(out, fmt.Sprintf("%s -> %s:%s [%s]", u.App, u.Type, u.ID, strings.Join(u.Files, " ")))
	}
	sort.Strings(out)
	return out
}

func TestCompute(t *testing.T) {
	t.Run("the fixture monorepo's edges match its declared ground truth", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		usages, err := Compute(root, monorepoApps(), monorepoDeclarations(root))
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}

		want := []string{
			"api -> LINK_TYPE_BUCKET:uploads [apps/api/src/server.ts]",
			"api -> LINK_TYPE_POSTGRES:main-db [apps/api/src/reports.ts apps/api/src/server.ts]",
			"api -> LINK_TYPE_POSTGRES:tenant-db [apps/api/src/server.ts]",
			"worker -> LINK_TYPE_POSTGRES:analytics-db [apps/worker/src/jobs.ts apps/worker/src/worker.ts]",
			"worker -> LINK_TYPE_POSTGRES:main-db [apps/worker/src/worker.ts]",
		}
		if got := edgeStrings(usages); !reflect.DeepEqual(got, want) {
			t.Errorf("edges =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	})

	t.Run("a side-effect-only import grants no usage edge", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		usages, err := Compute(root, monorepoApps(), monorepoDeclarations(root))
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}

		for _, u := range usages {
			if u.ID == "audit-db" {
				t.Errorf("audit-db reached %q through %v, but apps/api/src/boot.ts only imports it for effect", u.App, u.Files)
			}
		}
	})

	t.Run("a barrel re-export grants no usage edge for the exports it does not use", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		usages, err := Compute(root, monorepoApps(), monorepoDeclarations(root))
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}

		for _, u := range usages {
			if u.App == "worker" && u.ID == "uploads" {
				t.Errorf("uploads reached worker through %v, but worker only takes db from the barrel", u.Files)
			}
		}
	})

	t.Run("a file inside an app that only re-exports the handle grants nothing", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		usages, err := Compute(root, monorepoApps(), monorepoDeclarations(root))
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}

		for _, u := range usages {
			if slices.Contains(u.Files, "apps/worker/src/reexport.ts") || slices.Contains(u.Files, "apps/api/src/db-alias.ts") {
				t.Errorf("%s:%s is provenanced to a conduit file: %v", u.Type, u.ID, u.Files)
			}
		}
	})

	t.Run("no app source to attribute against leaves the declaration unplaced rather than refused", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		usages, err := Compute(root, []App{{Name: "api"}}, monorepoDeclarations(root))
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}
		if len(usages) != 0 {
			t.Errorf("usages = %v, want none for a path-less app", edgeStrings(usages))
		}
	})

	t.Run("JSX in a .js file reads as the framework reads it", func(t *testing.T) {
		root := fixtureRoot(t, "jsx-in-js")

		usages, err := Compute(root, []App{{Name: "web", Path: "apps/web"}}, []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "metrics-db", Stack: stackAt(root, "shared/metrics.ts")},
		})
		if err != nil {
			t.Fatalf("Compute err = %v", err)
		}

		want := []string{"web -> LINK_TYPE_POSTGRES:metrics-db [apps/web/pages/index.js]"}
		if got := edgeStrings(usages); !reflect.DeepEqual(got, want) {
			t.Errorf("edges = %v, want %v", got, want)
		}
	})

	t.Run("a runtime-computed import specifier fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "computed-import")

		_, err := Compute(root, []App{{Name: "worker", Path: "apps/worker"}}, []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "metrics-db", Stack: stackAt(root, "shared/metrics.ts")},
		})

		var unresolved *UnresolvedImportError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedImportError", err)
		}
		if unresolved.App != "worker" {
			t.Errorf("App = %q, want %q", unresolved.App, "worker")
		}
		if unresolved.File != "apps/worker/src/worker.ts" {
			t.Errorf("File = %q, want the file holding the computed specifier", unresolved.File)
		}
		if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "apps/worker/src/worker.ts") {
			t.Errorf("err = %v, want it to name the app and the file", err)
		}
	})

	t.Run("a declaration with no locatable source fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		_, err := Compute(root, monorepoApps(), []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "main-db", Stack: "Error\n    at node:internal/modules/esm/module_job:271:25"},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
		if unresolved.ID != "main-db" {
			t.Errorf("ID = %q, want %q", unresolved.ID, "main-db")
		}
		if !strings.Contains(err.Error(), "main-db") {
			t.Errorf("err = %v, want it to name the resource", err)
		}
	})

	t.Run("an empty stack fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		_, err := Compute(root, monorepoApps(), []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "main-db"},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
	})

	t.Run("a declaration outside the project root fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		_, err := Compute(root, monorepoApps(), []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, ID: "main-db", Stack: stackAt(t.TempDir(), "elsewhere.ts")},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
	})
}
