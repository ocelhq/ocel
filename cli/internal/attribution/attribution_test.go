package attribution

import (
	"errors"
	"fmt"
	"os"
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
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: stackAt(root, "shared/db.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "analytics-db", Stack: stackAt(root, "shared/analytics.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "audit-db", Stack: stackAt(root, "shared/audit.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "tenant-db", Stack: stackAt(root, "shared/tenant.ts")},
		{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads", Stack: stackAt(root, "shared/files.ts")},
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
		out = append(out, fmt.Sprintf("%s -> %s:%s [%s]", u.App, u.Type, u.Name, strings.Join(u.Files, " ")))
	}
	sort.Strings(out)
	return out
}

func TestAContainerAppIsAttributedWithoutASecondInstallOnTheDevelopersDisk(t *testing.T) {
	root := fixtureRoot(t, "uninstalled")
	declarations := []Declaration{{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: stackAt(root, "shared/db.ts")}}

	serverless := []App{{Name: "web", Path: "apps/web"}}
	if _, err := Compute(root, serverless, declarations); err == nil {
		t.Fatal("Compute() over a serverless app read an import graph its node_modules cannot resolve, so the fixture proves nothing")
	}

	usages, err := Compute(root, []App{{Name: "web", Path: "apps/web", Container: true}}, declarations)
	if err != nil {
		t.Fatalf("Compute() over a container app = %v — the image installs the app's dependencies, and the deploy already proved it", err)
	}

	if want := []string{"web -> LINK_TYPE_POSTGRES:main-db [apps/web/src/server.ts]"}; !reflect.DeepEqual(edgeStrings(usages), want) {
		t.Errorf("edges = %v, want %v — the graph of the app's own files is what attribution needs", edgeStrings(usages), want)
	}
}

func workspaceFixture(t *testing.T, installed bool) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"package.json":                 `{"name":"root","workspaces":["apps/*","packages/*"]}`,
		"sdk/index.ts":                 "export function postgres(id: string) {\n  return { id, kind: \"postgres\" as const };\n}\n",
		"packages/shared/package.json": `{"name":"@fixture/shared","main":"index.ts"}`,
		"packages/shared/index.ts":     "import { postgres } from \"../../sdk/index.js\";\n\nexport const mainDb = postgres(\"main-db\");\n",
		"apps/web/package.json":        `{"name":"@fixture/web"}`,
		"apps/web/src/server.ts":       "import express from \"express\";\n\nimport { mainDb } from \"@fixture/shared\";\n\nexport const app = express().get(\"/\", () => mainDb.id);\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if installed {
		linked := filepath.Join(root, "node_modules", "@fixture", "shared")
		if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "packages", "shared"), linked); err != nil {
			t.Skipf("this machine makes no symlinks, and a workspace member is linked into node_modules: %v", err)
		}
	}
	return root
}

func TestAContainerAppsWorkspaceMembersAreReadRatherThanAssumedInstalled(t *testing.T) {
	members := []string{"@fixture/shared", "@fixture/web"}

	t.Run("a member the developer has not installed stops the deploy", func(t *testing.T) {
		root := workspaceFixture(t, false)

		_, err := Compute(root, []App{{Name: "web", Path: "apps/web", Container: true, Members: members}},
			[]Declaration{{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: stackAt(root, "packages/shared/index.ts")}})
		if err == nil {
			t.Fatal("Compute() read a workspace member as a registry package the image installs, so every resource the member declares reaches the app with no edge and no complaint")
		}
		if !strings.Contains(err.Error(), "@fixture/shared") {
			t.Errorf("Compute() = %v, and the reader is never told which import ocel could not follow", err)
		}
	})

	t.Run("a member linked into node_modules is followed to what it declares", func(t *testing.T) {
		root := workspaceFixture(t, true)

		usages, err := Compute(root, []App{{Name: "web", Path: "apps/web", Container: true, Members: members}},
			[]Declaration{{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: stackAt(root, "packages/shared/index.ts")}})
		if err != nil {
			t.Fatalf("Compute() = %v", err)
		}
		if want := []string{"web -> LINK_TYPE_POSTGRES:main-db [apps/web/src/server.ts]"}; !reflect.DeepEqual(edgeStrings(usages), want) {
			t.Errorf("edges = %v, want %v — the app reaches the resource through a package of its own workspace", edgeStrings(usages), want)
		}
	})

	t.Run("a package from the registry is still left to the image to install", func(t *testing.T) {
		root := workspaceFixture(t, true)

		if _, err := Compute(root, []App{{Name: "web", Path: "apps/web", Container: true, Members: members}}, nil); err != nil {
			t.Errorf("Compute() = %v, and express is installed by the image rather than declared by this workspace", err)
		}
	})
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
			if u.Name == "audit-db" {
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
			if u.App == "worker" && u.Name == "uploads" {
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
				t.Errorf("%s:%s is provenanced to a conduit file: %v", u.Type, u.Name, u.Files)
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
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "metrics-db", Stack: stackAt(root, "shared/metrics.ts")},
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
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "metrics-db", Stack: stackAt(root, "shared/metrics.ts")},
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
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: "Error\n    at node:internal/modules/esm/module_job:271:25"},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
		if unresolved.Name != "main-db" {
			t.Errorf("Name = %q, want %q", unresolved.Name, "main-db")
		}
		if !strings.Contains(err.Error(), "main-db") {
			t.Errorf("err = %v, want it to name the resource", err)
		}
	})

	t.Run("an empty stack fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		_, err := Compute(root, monorepoApps(), []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db"},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
	})

	t.Run("a declaration outside the project root fails closed", func(t *testing.T) {
		root := fixtureRoot(t, "monorepo")

		_, err := Compute(root, monorepoApps(), []Declaration{
			{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main-db", Stack: stackAt(t.TempDir(), "elsewhere.ts")},
		})

		var unresolved *UnresolvedDeclarationError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedDeclarationError", err)
		}
	})
}
