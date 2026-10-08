package build

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestFrameworkBuildID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		app      string
		contents map[string]string
		want     string
	}{
		{
			name:     "reads hosting.json every framework writes",
			app:      "api",
			contents: map[string]string{"api/" + buildoutput.HostingFile: `{"framework":"node","frameworkBuildId":"0123456789abcdef"}`},
			want:     "0123456789abcdef",
		},
		{
			name:     "next states its own build id there too",
			app:      "web",
			contents: map[string]string{"web/" + buildoutput.HostingFile: `{"framework":"next","frameworkBuildId":"UxK1p2"}`},
			want:     "UxK1p2",
		},
		{
			name:     "a routing manifest alone answers nothing",
			app:      "web",
			contents: map[string]string{"web/routing-manifest.json": `{"buildId":"stale"}`},
			want:     "",
		},
		{
			name: "an app that was never built answers nothing",
			app:  "api",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			for rel, contents := range tt.contents {
				writeAppFile(t, root, rel, []byte(contents))
			}
			got, err := FrameworkBuildID(root, tt.app)
			if err != nil {
				t.Fatalf("FrameworkBuildID(%q) = %v", tt.app, err)
			}
			if got != tt.want {
				t.Errorf("FrameworkBuildID(%q) = %q, want %q", tt.app, got, tt.want)
			}
		})
	}
}

func TestAnUnreadableHostingFailsTheFrameworkBuildIDRatherThanReadingAsNone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeAppFile(t, root, "api/"+buildoutput.HostingFile, []byte("not json"))

	if got, err := FrameworkBuildID(root, "api"); err == nil {
		t.Errorf("FrameworkBuildID = %q, nil over a hosting.json that is not JSON, want the corruption reported rather than recorded as no build", got)
	}
	if apps, err := EdgeApps(root); err == nil {
		t.Errorf("EdgeApps = %v, nil over a hosting.json that is not JSON, want the corruption reported rather than read as no edge need", apps)
	}
}

func TestEdgeApps(t *testing.T) {
	t.Parallel()

	t.Run("names every built app needing edge-runtime or edge-middleware", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeAppFile(t, root, "web/"+buildoutput.HostingFile,
			[]byte(`{"framework":"next","needs":{"edge-runtime":{"count":1,"routes":["/edgy"]}}}`))
		writeAppFile(t, root, "admin/"+buildoutput.HostingFile,
			[]byte(`{"framework":"next","needs":{"edge-middleware":{"count":1,"matchers":[]}}}`))
		writeAppFile(t, root, "docs/"+buildoutput.HostingFile,
			[]byte(`{"framework":"next","needs":{"edge-cache":{"count":4},"streaming":{"count":2}}}`))
		writeAppFile(t, root, "api/"+buildoutput.HostingFile, []byte(`{"framework":"node","needs":{}}`))

		apps, err := EdgeApps(root)
		if err != nil {
			t.Fatalf("EdgeApps = %v", err)
		}
		if !slices.Equal(apps, []string{"admin", "web"}) {
			t.Errorf("EdgeApps = %v, want only the apps needing edge code", apps)
		}
	})

	t.Run("an edge bundle on disk names nothing on its own", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeAppFile(t, root, "web/"+edge.AppBundleFile, []byte(`{"version":2}`))
		writeAppFile(t, root, "web/"+buildoutput.HostingFile, []byte(`{"framework":"next","needs":{}}`))

		if apps, err := EdgeApps(root); err != nil || len(apps) != 0 {
			t.Errorf("EdgeApps = %v, want the needs to decide, not the bundle", apps)
		}
	})

	t.Run("a project that was never built names nothing", func(t *testing.T) {
		t.Parallel()

		if apps, err := EdgeApps(t.TempDir()); err != nil || len(apps) != 0 {
			t.Errorf("EdgeApps = %v, want no apps before a build", apps)
		}
	})
}

func writeAppFile(t *testing.T, root, rel string, contents []byte) {
	t.Helper()
	dest := filepath.Join(root, statedir.Name, "output", "apps", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFunctions(t *testing.T) {
	t.Parallel()

	t.Run("no output tree errors", func(t *testing.T) {
		t.Parallel()

		_, err := ReadFunctions(t.TempDir())
		if err == nil {
			t.Fatal("CollectFunctions succeeded with no build output, want error")
		}
		if !strings.Contains(err.Error(), filepath.Join(statedir.Name, "output")) {
			t.Errorf("error = %q, want it to name the missing output directory", err)
		}
		if !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("error = %q, want it to point at `ocel build`", err)
		}
	})

	t.Run("an empty output tree is not an error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, statedir.Name, "output"), 0o755); err != nil {
			t.Fatal(err)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if fns != nil {
			t.Errorf("CollectFunctions = %+v, want nil", fns)
		}
	})

	t.Run("reads a prebuilt tree", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		outDir := filepath.Join(root, statedir.Name, "output")
		writeFuncConfig(t, outDir, "web", "index.func",
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
		writeFuncConfig(t, outDir, "web", filepath.Join("api", "todos", "[id].func"),
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}

		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "api/todos/[id]", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", App: "web"},
			{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/index.func", App: "web"},
		})
	})

	t.Run("with no functions directory returns nothing", func(t *testing.T) {
		t.Parallel()

		fns, err := readFunctions(t.TempDir())
		if err != nil {
			t.Fatalf("collectFunctions: %v", err)
		}
		if fns != nil {
			t.Errorf("collectFunctions = %+v, want nil", fns)
		}
	})

	trees := []struct {
		name  string
		setup func(t *testing.T, outDir string)
		want  []Function
	}{
		{
			name: "nested routes are collected without descending into a function's own tree",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", filepath.Join("api", "todos", "[id].func"),
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
				writeFuncConfig(t, outDir, "web", "index.func",
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
				if err := os.MkdirAll(filepath.Join(outDir, "apps", "web", "functions", "index.func", "node_modules", "dep"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: []Function{
				{Route: "api/todos/[id]", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", App: "web"},
				{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/index.func", App: "web"},
			},
		},
		{
			name: "the same route in two apps does not collide",
			setup: func(t *testing.T, outDir string) {
				for _, app := range []string{"admin", "storefront"} {
					writeFuncConfig(t, outDir, app, filepath.Join("api", "documents.func"),
						buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "route.js", ID: "/api/documents", App: app})
				}
			},
			want: []Function{
				{Route: "api/documents", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "route.js", ArtifactPath: "apps/admin/functions/api/documents.func", RouteID: "/api/documents", App: "admin"},
				{Route: "api/documents", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "route.js", ArtifactPath: "apps/storefront/functions/api/documents.func", RouteID: "/api/documents", App: "storefront"},
			},
		},
	}
	for _, tt := range trees {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir := t.TempDir()
			tt.setup(t, outDir)

			fns, err := readFunctions(outDir)
			if err != nil {
				t.Fatalf("collectFunctions: %v", err)
			}
			assertFunctions(t, "collectFunctions", fns, tt.want)
		})
	}

	t.Run("function-config.json id flows into the function", func(t *testing.T) {
		t.Parallel()

		outDir := t.TempDir()
		writeFuncConfig(t, outDir, "web", filepath.Join("api", "documents.func"),
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "route.js", ID: "/api/documents", App: "web"})

		fns, err := readFunctions(outDir)
		if err != nil {
			t.Fatalf("collectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("got %d functions, want 1", len(fns))
		}
		if got, want := fns[0].RouteID, "/api/documents"; got != want {
			t.Errorf("RouteID = %q, want %q (function-config.json id must flow into the function)", got, want)
		}
	})

	t.Run("function-config.json app flows into the function", func(t *testing.T) {
		t.Parallel()

		outDir := t.TempDir()
		writeFuncConfig(t, outDir, "storefront", "index.func",
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", App: "storefront"})

		fns, err := readFunctions(outDir)
		if err != nil {
			t.Fatalf("collectFunctions: %v", err)
		}
		if got, want := fns[0].App, "storefront"; got != want {
			t.Errorf("App = %q, want %q (function-config.json app must flow into the function)", got, want)
		}
	})

	malformed := []struct {
		name      string
		setup     func(t *testing.T, outDir string)
		succeeded string
		wants     []string
		wantMsg   string
	}{
		{
			name: "a .func with no function-config.json errors",
			setup: func(t *testing.T, outDir string) {
				if err := os.MkdirAll(filepath.Join(outDir, "apps", "web", "functions", "api.func"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			succeeded: "collectFunctions succeeded on a .func with no function-config.json, want error",
			wants:     []string{"api.func", buildoutput.FunctionConfigFile},
			wantMsg:   "want it to name the offending .func and function-config.json",
		},
		{
			name: "a config missing its framework errors",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", "api.func", buildoutput.FunctionConfig{EntryFile: "index.handler", App: "web"})
			},
			succeeded: "collectFunctions succeeded on config missing its framework, want error",
			wants:     []string{"requires framework, entryFile, and app"},
			wantMsg:   "want it to explain the required fields",
		},
		{
			name: "a config missing app errors",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", "index.func",
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler"})
			},
			succeeded: "collectFunctions succeeded on config missing app, want error",
			wants:     []string{"requires framework, entryFile, and app"},
			wantMsg:   "want it to explain the required fields",
		},
		{
			name: "invalid function-config.json JSON errors",
			setup: func(t *testing.T, outDir string) {
				dir := filepath.Join(outDir, "apps", "web", "functions", "api.func")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, buildoutput.FunctionConfigFile), []byte("not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			succeeded: "collectFunctions succeeded on invalid JSON, want error",
			wants:     []string{"invalid " + buildoutput.FunctionConfigFile},
			wantMsg:   "want it to flag invalid function-config.json",
		},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir := t.TempDir()
			tt.setup(t, outDir)

			_, err := readFunctions(outDir)
			if err == nil {
				t.Fatal(tt.succeeded)
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, %s", err, tt.wantMsg)
				}
			}
		})
	}
}
