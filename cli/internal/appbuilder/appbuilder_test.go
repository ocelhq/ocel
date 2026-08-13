package appbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
)

func lookup(env []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		if rest, ok := strings.CutPrefix(entry, name+"="); ok {
			value, found = rest, true
		}
	}
	return value, found
}

func writeBuilder(t *testing.T, projectDir string) string {
	t.Helper()
	path := node.BuilderPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFuncConfig(t *testing.T, outDir, app, funcRel string, cfg functionConfig) {
	t.Helper()
	dir := filepath.Join(outDir, appsDirName, app, functionsDirName, funcRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFunctions(t *testing.T, label string, got, want []manifestbuilder.Function) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s returned %d functions, want %d: %+v", label, len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("function[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func expressFixture(t *testing.T) string {
	t.Helper()
	fixtureRoot := repoRelPath(t, "cli", "node", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, ".ocel")) })
	if err := node.Ensure(fixtureRoot); err != nil {
		t.Fatalf("node.Ensure: %v", err)
	}
	return fixtureRoot
}

func repoRelPath(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	return filepath.Join(append([]string{repoRoot}, parts...)...)
}

func TestBuild(t *testing.T) {
	t.Run("runs the builder and discovers the functions it wrote", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		builderPath := writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "api", Path: "apps/api", Framework: "express", Entrypoint: "src/server.ts", Compute: "serverless"},
				{Name: "worker", Path: "apps/worker", Framework: "express", Compute: "serverless"},
			},
		}

		var gotScript string
		var gotReq builderRequest
		var gotEnv []string
		builder := Builder{Exec: func(_ context.Context, scriptPath string, env []string, request []byte, _ io.Writer) error {
			gotScript = scriptPath
			gotEnv = env
			if err := json.Unmarshal(request, &gotReq); err != nil {
				return err
			}
			writeFuncConfig(t, gotReq.OutDir, "api", "index.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "api"})
			writeFuncConfig(t, gotReq.OutDir, "worker", "index.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "worker"})
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		fns, err := CollectFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}

		assertFunctions(t, "CollectFunctions", fns, []manifestbuilder.Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/api/functions/index.func", Framework: "express", App: "api"},
			{Name: "index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/worker/functions/index.func", Framework: "express", App: "worker"},
		})

		if got, want := gotReq.OutDir, filepath.Join(root, ".ocel", "output"); got != want {
			t.Errorf("request outDir = %q, want %q", got, want)
		}
		if got, want := gotReq.ProjectRoot, root; got != want {
			t.Errorf("request projectRoot = %q, want %q", got, want)
		}
		if len(gotReq.Apps) != 2 {
			t.Fatalf("request had %d apps, want 2", len(gotReq.Apps))
		}
		if got, want := gotReq.Apps[0].Cwd, filepath.Join(root, "apps/api"); got != want {
			t.Errorf("app[0].cwd = %q, want %q", got, want)
		}
		if got, want := gotReq.Apps[0].Entrypoint, "src/server.ts"; got != want {
			t.Errorf("app[0].entrypoint = %q, want %q", got, want)
		}
		if got, want := gotReq.Apps[0].Framework, "express"; got != want {
			t.Errorf("app[0].framework = %q, want %q", got, want)
		}
		if gotReq.Apps[1].Entrypoint != "" {
			t.Errorf("app[1].entrypoint = %q, want empty", gotReq.Apps[1].Entrypoint)
		}

		if gotScript != builderPath {
			t.Errorf("script path = %q, want %q", gotScript, builderPath)
		}
		if got, _ := lookup(gotEnv, "NEXT_ADAPTER_PATH"); got != node.AdapterPath(root) {
			t.Errorf("adapter path = %q, want %q", got, node.AdapterPath(root))
		}
	})

	t.Run("names the missing builder when none was materialized", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
		}

		err := Build(context.Background(), cfg, nil, io.Discard)
		if err == nil {
			t.Fatal("Build succeeded with no materialized builder, want error")
		}
		if !strings.Contains(err.Error(), node.BuilderPath(root)) {
			t.Errorf("error = %q, want it to name the missing builder path", err)
		}
	})

	t.Run("with no apps runs the builder for detection and resets output", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		writeFuncConfig(t, filepath.Join(root, ".ocel", "output"), "stale", "index.func",
			functionConfig{Runtime: "nodejs24.x", Handler: "h", Framework: "express", App: "stale"})

		var gotReq builderRequest
		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
			return json.Unmarshal(request, &gotReq)
		}}

		if err := builder.Build(context.Background(), &projectconfig.Config{Dir: root}, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		fns, err := CollectFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if fns != nil {
			t.Errorf("CollectFunctions returned %+v, want nil", fns)
		}
		if len(gotReq.Apps) != 0 {
			t.Errorf("request apps = %+v, want empty", gotReq.Apps)
		}
		if got, want := gotReq.ProjectRoot, root; got != want {
			t.Errorf("request projectRoot = %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(root, ".ocel", "output", appsDirName, "stale")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale .func survived the reset (stat err = %v)", err)
		}
	})

	t.Run("a build failure returns a clear error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
		}

		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, _ []byte, _ io.Writer) error {
			return errors.New("node-builder failed: no entrypoint resolved for app \"api\"")
		}}

		err := builder.Build(context.Background(), cfg, nil, io.Discard)
		if err == nil {
			t.Fatal("Build succeeded, want error")
		}
		if !strings.Contains(err.Error(), "no entrypoint resolved") {
			t.Errorf("error = %q, want it to surface the node-builder failure", err)
		}
	})

	t.Run("exports resolved values into the build environment", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)

		var got []string
		builder := Builder{Exec: func(_ context.Context, _ string, env []string, _ []byte, _ io.Writer) error {
			got = env
			return nil
		}}

		vars := map[string]map[string]string{"": {"POSTHOG_ID": "ph-123"}}
		if err := builder.Build(context.Background(), &projectconfig.Config{Dir: root}, vars, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if value, _ := lookup(got, "POSTHOG_ID"); value != "ph-123" {
			t.Fatalf("build environment POSTHOG_ID = %q, want the resolved value", value)
		}
	})

	t.Run("sends each app its own values and folder", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)

		var got builderRequest
		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
			return json.Unmarshal(request, &got)
		}}

		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "storefront", Path: "apps/storefront", Framework: "next", Folder: "/storefront"},
				{Name: "admin", Path: "apps/admin", Framework: "next", Folder: "/admin"},
			},
		}
		vars := map[string]map[string]string{
			"storefront": {"POSTHOG_ID": "ph-store"},
			"admin":      {"POSTHOG_ID": "ph-admin"},
		}
		if err := builder.Build(context.Background(), cfg, vars, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if len(got.Apps) != 2 {
			t.Fatalf("request carried %d apps, want both", len(got.Apps))
		}
		for _, app := range got.Apps {
			if app.Env["POSTHOG_ID"] != vars[app.Name]["POSTHOG_ID"] {
				t.Errorf("%s POSTHOG_ID = %q, want %q", app.Name, app.Env["POSTHOG_ID"], vars[app.Name]["POSTHOG_ID"])
			}
		}
	})

	t.Run("with two folders each build states its own binding", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)

		var got builderRequest
		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
			return json.Unmarshal(request, &got)
		}}

		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "web", Path: "apps/web", Framework: "next", Folder: "/web"},
				{Name: "admin", Path: "apps/admin", Framework: "next", Folder: "/admin"},
			},
		}
		if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		folders := make(map[string]string, len(got.Apps))
		for _, app := range got.Apps {
			folders[app.Name] = app.Folder
		}
		if folders["web"] != "/web" || folders["admin"] != "/admin" {
			t.Errorf("folders = %v, want each app the binding it declares", folders)
		}
	})

	t.Run("refuses a resolved value the build environment owns", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"PATH", "NEXT_ADAPTER_PATH", "OCEL_APP_FOLDER"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeBuilder(t, root)

				ran := false
				builder := Builder{Exec: func(_ context.Context, _ string, _ []string, _ []byte, _ io.Writer) error {
					ran = true
					return nil
				}}

				cfg := &projectconfig.Config{
					Dir:  root,
					Apps: []projectconfig.App{{Name: "web", Path: "apps/web", Framework: "next"}},
				}
				vars := map[string]map[string]string{"web": {name: "hijacked"}}
				err := builder.Build(context.Background(), cfg, vars, io.Discard)
				if err == nil {
					t.Fatalf("Build succeeded with a variable declared as %s, want a refusal", name)
				}
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error = %q, want it to name %q", err, name)
				}
				if ran {
					t.Error("the builder ran, want the refusal before anything is built")
				}
			})
		}
	})

	t.Run("the builder itself is bound to the project root", func(t *testing.T) {
		root := t.TempDir()
		writeBuilder(t, root)
		t.Setenv("OCEL_APP_FOLDER", "/stale")

		var got []string
		builder := Builder{Exec: func(_ context.Context, _ string, env []string, _ []byte, _ io.Writer) error {
			got = env
			return nil
		}}

		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "web", Path: "apps/web", Framework: "next", Folder: "/web"}},
		}
		if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		value, found := lookup(got, "OCEL_APP_FOLDER")
		if !found {
			t.Fatal("no binding was stated, so a stale one from the parent environment still answers")
		}
		if value != "" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the project root", value)
		}
	})

	t.Run("over real node, builds the fixture app it is configured with", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: spawns real node over the builder")
		}

		fixtureRoot := expressFixture(t)
		cfg := &projectconfig.Config{
			Dir:  fixtureRoot,
			Apps: []projectconfig.App{{Name: "api", Path: ".", Framework: "express", Compute: "serverless"}},
		}

		var stderr bytes.Buffer
		if err := Build(context.Background(), cfg, nil, &stderr); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := CollectFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("CollectFunctions returned %d functions, want 1: %+v", len(fns), fns)
		}
		want := manifestbuilder.Function{
			Name:         "index",
			Runtime:      "nodejs24.x",
			Handler:      "src/server.js",
			ArtifactPath: "apps/api/functions/index.func",
			Framework:    "express",
			App:          "api",
		}
		if fns[0] != want {
			t.Errorf("function = %+v, want %+v", fns[0], want)
		}
	})

	t.Run("over real node, detects a single app from an unconfigured project", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: spawns real node over the builder")
		}

		fixtureRoot := expressFixture(t)

		var stderr bytes.Buffer
		if err := Build(context.Background(), &projectconfig.Config{Dir: fixtureRoot}, nil, &stderr); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := CollectFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("CollectFunctions returned %d functions, want 1: %+v", len(fns), fns)
		}
		if fns[0].Name != "index" || fns[0].Framework != "express" {
			t.Errorf("detected function = %+v, want name index framework express", fns[0])
		}
		if fns[0].App != "express-app" {
			t.Errorf("detected function app = %q, want %q", fns[0].App, "express-app")
		}
	})
}

func TestCollectFunctions(t *testing.T) {
	t.Parallel()

	t.Run("no output tree errors", func(t *testing.T) {
		t.Parallel()

		_, err := CollectFunctions(t.TempDir())
		if err == nil {
			t.Fatal("CollectFunctions succeeded with no build output, want error")
		}
		if !strings.Contains(err.Error(), filepath.Join(scratchDirName, outputDirName)) {
			t.Errorf("error = %q, want it to name the missing output directory", err)
		}
		if !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("error = %q, want it to point at `ocel build`", err)
		}
	})

	t.Run("an empty output tree is not an error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, scratchDirName, outputDirName), 0o755); err != nil {
			t.Fatal(err)
		}

		fns, err := CollectFunctions(root)
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
		outDir := filepath.Join(root, scratchDirName, outputDirName)
		writeFuncConfig(t, outDir, "web", "index.func",
			functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
		writeFuncConfig(t, outDir, "web", filepath.Join("api", "todos", "[id].func"),
			functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})

		fns, err := CollectFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}

		assertFunctions(t, "CollectFunctions", fns, []manifestbuilder.Function{
			{Name: "api/todos/[id]", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", Framework: "next", App: "web"},
			{Name: "index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/index.func", Framework: "next", App: "web"},
		})
	})

	t.Run("with no functions directory returns nothing", func(t *testing.T) {
		t.Parallel()

		fns, err := collectFunctions(t.TempDir())
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
		want  []manifestbuilder.Function
	}{
		{
			name: "nested routes are collected without descending into a function's own tree",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", filepath.Join("api", "todos", "[id].func"),
					functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
				writeFuncConfig(t, outDir, "web", "index.func",
					functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
				if err := os.MkdirAll(filepath.Join(outDir, appsDirName, "web", "functions", "index.func", "node_modules", "dep"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: []manifestbuilder.Function{
				{Name: "api/todos/[id]", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", Framework: "next", App: "web"},
				{Name: "index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/index.func", Framework: "next", App: "web"},
			},
		},
		{
			name: "the same route in two apps does not collide",
			setup: func(t *testing.T, outDir string) {
				for _, app := range []string{"admin", "storefront"} {
					writeFuncConfig(t, outDir, app, filepath.Join("api", "documents.func"),
						functionConfig{Runtime: "nodejs24.x", Handler: "route.js", Framework: "next", ID: "/api/documents", App: app})
				}
			},
			want: []manifestbuilder.Function{
				{Name: "api/documents", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "apps/admin/functions/api/documents.func", Framework: "next", RouteID: "/api/documents", App: "admin"},
				{Name: "api/documents", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "apps/storefront/functions/api/documents.func", Framework: "next", RouteID: "/api/documents", App: "storefront"},
			},
		},
	}
	for _, tt := range trees {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir := t.TempDir()
			tt.setup(t, outDir)

			fns, err := collectFunctions(outDir)
			if err != nil {
				t.Fatalf("collectFunctions: %v", err)
			}
			assertFunctions(t, "collectFunctions", fns, tt.want)
		})
	}

	t.Run("config.json id flows into the function", func(t *testing.T) {
		t.Parallel()

		outDir := t.TempDir()
		writeFuncConfig(t, outDir, "web", filepath.Join("api", "documents.func"),
			functionConfig{Runtime: "nodejs24.x", Handler: "route.js", Framework: "next", ID: "/api/documents", App: "web"})

		fns, err := collectFunctions(outDir)
		if err != nil {
			t.Fatalf("collectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("got %d functions, want 1", len(fns))
		}
		if got, want := fns[0].RouteID, "/api/documents"; got != want {
			t.Errorf("RouteID = %q, want %q (config.json id must flow into the function)", got, want)
		}
	})

	t.Run("config.json app flows into the function", func(t *testing.T) {
		t.Parallel()

		outDir := t.TempDir()
		writeFuncConfig(t, outDir, "storefront", "index.func",
			functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "storefront"})

		fns, err := collectFunctions(outDir)
		if err != nil {
			t.Fatalf("collectFunctions: %v", err)
		}
		if got, want := fns[0].App, "storefront"; got != want {
			t.Errorf("App = %q, want %q (config.json app must flow into the function)", got, want)
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
			name: "a .func with no config.json errors",
			setup: func(t *testing.T, outDir string) {
				if err := os.MkdirAll(filepath.Join(outDir, appsDirName, "web", "functions", "api.func"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			succeeded: "collectFunctions succeeded on a .func with no config.json, want error",
			wants:     []string{"api.func", configFileName},
			wantMsg:   "want it to name the offending .func and config.json",
		},
		{
			name: "a config missing framework errors",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", "api.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", App: "web"})
			},
			succeeded: "collectFunctions succeeded on config missing framework, want error",
			wants:     []string{"requires runtime, handler, framework, and app"},
			wantMsg:   "want it to explain the required fields",
		},
		{
			name: "a config missing app errors",
			setup: func(t *testing.T, outDir string) {
				writeFuncConfig(t, outDir, "web", "index.func",
					functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express"})
			},
			succeeded: "collectFunctions succeeded on config missing app, want error",
			wants:     []string{"requires runtime, handler, framework, and app"},
			wantMsg:   "want it to explain the required fields",
		},
		{
			name: "invalid config.json JSON errors",
			setup: func(t *testing.T, outDir string) {
				dir := filepath.Join(outDir, appsDirName, "web", "functions", "api.func")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			succeeded: "collectFunctions succeeded on invalid JSON, want error",
			wants:     []string{"invalid " + configFileName},
			wantMsg:   "want it to flag invalid config.json",
		},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir := t.TempDir()
			tt.setup(t, outDir)

			_, err := collectFunctions(outDir)
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

func TestBuilderEnv(t *testing.T) {
	t.Parallel()

	t.Run("adds resolved values without losing the builder's own", func(t *testing.T) {
		t.Parallel()

		env := builderEnv("/adapters/next.js", map[string]string{"POSTHOG_ID": "ph-123"})

		if got, _ := lookup(env, "NEXT_ADAPTER_PATH"); got != "/adapters/next.js" {
			t.Errorf("NEXT_ADAPTER_PATH = %q, want the adapter path the builder needs", got)
		}
		if got, _ := lookup(env, "POSTHOG_ID"); got != "ph-123" {
			t.Errorf("POSTHOG_ID = %q, want the resolved value", got)
		}
		if len(env) <= 3 {
			t.Errorf("env holds %d entries, want the inherited environment as well", len(env))
		}
	})

	t.Run("what the build owns is applied last", func(t *testing.T) {
		t.Parallel()

		env := builderEnv("/adapters/next.js", map[string]string{
			"NEXT_ADAPTER_PATH": "/evil/adapter.js",
			"OCEL_APP_FOLDER":   "/admin",
		})

		if got, _ := lookup(env, "NEXT_ADAPTER_PATH"); got != "/adapters/next.js" {
			t.Errorf("NEXT_ADAPTER_PATH = %q, want the builder's own adapter", got)
		}
		if got, _ := lookup(env, "OCEL_APP_FOLDER"); got != "" {
			t.Errorf("OCEL_APP_FOLDER = %q, want the project root the builder process runs under", got)
		}
	})
}

func TestFailureSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "leading warnings do not headline the failure",
			output: ` ⚠ The "middleware" file convention is deprecated. Please use "proxy" instead.
The "id" argument must be of type string. Received undefined
Next.js build worker exited with code: 1 and signal: null
`,
			want: "The \"id\" argument must be of type string. Received undefined\nNext.js build worker exited with code: 1 and signal: null",
		},
		{
			name:   "single line",
			output: "no entrypoint resolved for app \"api\"\n",
			want:   "no entrypoint resolved for app \"api\"",
		},
		{
			name:   "empty output",
			output: "   \n\n",
			want:   "",
		},
		{
			name:   "decorative lines never take the tail",
			output: "Error: adapter threw\n────────────\n   ▲   \n===\n",
			want:   "Error: adapter threw",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := failureSummary(tt.output)
			if got != tt.want {
				t.Errorf("failureSummary() = %q, want %q", got, tt.want)
			}
			if first, _, _ := strings.Cut(got, "\n"); strings.Contains(first, "deprecated") {
				t.Errorf("summary headlines a deprecation warning: %q", first)
			}
		})
	}
}

func runNodeScript(t *testing.T, source string) error {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	path := filepath.Join(t.TempDir(), "builder.mjs")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return runNode(context.Background(), path, os.Environ(), []byte("{}"), io.Discard)
}

func TestRunNode(t *testing.T) {
	t.Parallel()

	t.Run("a stdout-only failure is not dropped", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "console.log('adapter could not resolve the entrypoint');process.exit(1);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "adapter could not resolve the entrypoint") {
			t.Errorf("error = %q, want it to carry the failure the builder reported on stdout", err)
		}
	})

	t.Run("names the exit code", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "console.error('boom');process.exit(3);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "3") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("error = %q, want it to name exit status 3 and the failure", err)
		}
	})

	t.Run("a silent failure still errors", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "process.exit(1);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "node-builder failed") {
			t.Errorf("error = %q, want it to name the failing builder", err)
		}
	})

	t.Run("a protocol error record wins over the trailing-lines heuristic", func(t *testing.T) {
		t.Parallel()

		script := fmt.Sprintf(
			`console.log("noise that would otherwise headline the failure");
console.log(%s + JSON.stringify({type:"error",app:"api",stage:"build",message:%s}));
process.exit(1);
`,
			jsString(nodeprotocol.Prefix), jsString(`no entrypoint resolved for app "api"`))

		err := runNodeScript(t, script)
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), `no entrypoint resolved for app "api"`) {
			t.Errorf("error = %q, want the protocol error record's message, not the trailing noise", err)
		}
		if strings.Contains(err.Error(), "noise that would otherwise headline") {
			t.Errorf("error = %q, want the actual error, not the last non-blank lines", err)
		}
	})

	t.Run("a protocol-prefixed line that fails to parse is still forwarded, not swallowed", func(t *testing.T) {
		t.Parallel()

		script := fmt.Sprintf(`console.log(%s + "{not valid json");
process.exit(1);
`, jsString(nodeprotocol.Prefix))

		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node not on PATH")
		}
		path := filepath.Join(t.TempDir(), "builder.mjs")
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		err := runNode(context.Background(), path, os.Environ(), []byte("{}"), &stderr)
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(stderr.String(), nodeprotocol.Prefix+"{not valid json") {
			t.Errorf("stderr = %q, want the malformed protocol line forwarded verbatim", stderr.String())
		}
	})

	t.Run("stdout and stderr write concurrently without racing on the shared writer", func(t *testing.T) {
		t.Parallel()

		script := `
for (let i = 0; i < 4000; i++) {
  process.stdout.write("out " + i + "\n");
  process.stderr.write("err " + i + "\n");
}
`
		if err := runNodeScript(t, script); err != nil {
			t.Fatalf("runNode: %v", err)
		}
	})

	t.Run("a span_start/span_end pair for an app produces a span on the run", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		ctx, run, err := obs.Start(context.Background(), dir, "ocel build")
		if err != nil {
			t.Fatalf("obs.Start: %v", err)
		}

		script := fmt.Sprintf(`const emit = (r) => console.log(%s + JSON.stringify(r));
emit({type:"span_start",id:"1",app:"api",stage:"build"});
emit({type:"span_end",id:"1",ok:true});
`, jsString(nodeprotocol.Prefix))

		if _, lookErr := exec.LookPath("node"); lookErr != nil {
			t.Skip("node not on PATH")
		}
		path := filepath.Join(t.TempDir(), "builder.mjs")
		if writeErr := os.WriteFile(path, []byte(script), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		if err := runNode(ctx, path, os.Environ(), []byte("{}"), io.Discard); err != nil {
			t.Fatalf("runNode: %v", err)
		}
		if err := run.Close(); err != nil {
			t.Fatalf("run.Close: %v", err)
		}

		raw, err := os.ReadFile(strings.TrimSuffix(run.LogPath(), ".ndjson") + ".otlp.json")
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		if !strings.Contains(string(raw), `"name": "build"`) || !strings.Contains(string(raw), `"stringValue": "api"`) {
			t.Errorf("trace = %s, want a build span attributed to app api", raw)
		}
	})
}

// jsString renders a Go string as a JS string literal for the inline
// fixture scripts above.
func jsString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
