package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
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
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := node.BuilderPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePlan(t *testing.T, outDir string, summaries ...functionSummary) {
	t.Helper()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(buildPlan{Functions: summaries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, buildPlanFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFuncConfig(t *testing.T, outDir, app, funcRel string, cfg appbuild.FunctionConfig) {
	t.Helper()
	dir := filepath.Join(outDir, "apps", app, functionsDirName, funcRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, appbuild.FunctionConfigFile), data, 0o644); err != nil {
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
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, constants.ProjectStateDirName)) })
	if err := node.Ensure(fixtureRoot); err != nil {
		t.Fatalf("node.Ensure: %v", err)
	}
	return fixtureRoot
}

func repoRelPath(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{fixturetest.RepoDir(t)}, parts...)...)
}

func TestBuild(t *testing.T) {
	t.Run("runs the builder and discovers the functions it wrote", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		builderPath := writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "api", Path: "apps/api", Entrypoint: "src/server.ts", Framework: projectconfig.Framework{Name: "node"}},
				{Name: "worker", Path: "apps/worker"},
			},
		}

		var gotScript string
		var gotReq builderRequest
		var gotEnv []string
		builder := nodeOnly{node: func(_ context.Context, scriptPath string, env []string, request []byte, _ Log) error {
			gotScript = scriptPath
			gotEnv = env
			if err := json.Unmarshal(request, &gotReq); err != nil {
				return err
			}
			writeFuncConfig(t, gotReq.OutDir, "api", "index.func", appbuild.FunctionConfig{Framework: appbuild.Framework{Name: "node"}, Handler: "index.handler", App: "api"})
			writeFuncConfig(t, gotReq.OutDir, "worker", "index.func", appbuild.FunctionConfig{Framework: appbuild.Framework{Name: "node"}, Handler: "index.handler", App: "worker"})
			writePlan(t, gotReq.OutDir,
				functionSummary{Name: "api", Framework: appbuild.Framework{Name: "node"}, Handler: "index.handler", ArtifactPath: filepath.Join("apps", "api", "functions", "index.func"), Strategy: traceStrategy},
				functionSummary{Name: "worker", Framework: appbuild.Framework{Name: "node"}, Handler: "index.handler", ArtifactPath: filepath.Join("apps", "worker", "functions", "index.func"), Strategy: traceStrategy})
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}

		assertFunctions(t, "ReadFunctions", fns, []manifestbuilder.Function{
			{Route: "index", Framework: manifestbuilder.Framework{Name: "node"}, Handler: "index.handler", ArtifactPath: "apps/api/functions/index.func", App: "api"},
			{Route: "index", Framework: manifestbuilder.Framework{Name: "node"}, Handler: "index.handler", ArtifactPath: "apps/worker/functions/index.func", App: "worker"},
		})

		if got, want := gotReq.OutDir, filepath.Join(root, constants.ProjectStateDirName, "output"); got != want {
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
		if got := gotReq.Apps[0].Framework; got == nil || got.Name != "node" || got.Arch != "" {
			t.Errorf("app[0].framework = %+v, want the node framework with no arch", got)
		}
		if gotReq.Apps[1].Framework != nil {
			t.Errorf("app[1].framework = %+v, want the key left out when the app declares none", gotReq.Apps[1].Framework)
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
			Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}},
		}

		err := nodeOnly{node: runNode}.Build(context.Background(), cfg, nil, Log{})
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
		writeFuncConfig(t, filepath.Join(root, constants.ProjectStateDirName, "output"), "stale", "index.func",
			appbuild.FunctionConfig{Framework: appbuild.Framework{Name: "node"}, Handler: "h", App: "stale"})

		var gotReq builderRequest
		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, request []byte, _ Log) error {
			if err := json.Unmarshal(request, &gotReq); err != nil {
				return err
			}
			writePlan(t, gotReq.OutDir)
			return nil
		}}

		if err := builder.Build(context.Background(), &projectconfig.Config{Dir: root}, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		fns, err := ReadFunctions(root)
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
		if _, err := os.Stat(filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "stale")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale .func survived the reset (stat err = %v)", err)
		}
	})

	t.Run("a build failure returns a clear error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}},
		}

		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, _ []byte, _ Log) error {
			return errors.New("node-builder failed: no entrypoint resolved for app \"api\"")
		}}

		err := builder.Build(context.Background(), cfg, nil, Log{})
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
		builder := nodeOnly{node: func(_ context.Context, _ string, env []string, _ []byte, _ Log) error {
			got = env
			writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"))
			return nil
		}}

		vars := map[string]map[string]string{"": {"POSTHOG_ID": "ph-123"}}
		if err := builder.Build(context.Background(), &projectconfig.Config{Dir: root}, vars, Log{}); err != nil {
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
		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, request []byte, _ Log) error {
			writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"))
			return json.Unmarshal(request, &got)
		}}

		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "storefront", Path: "apps/storefront", Folder: "/storefront"},
				{Name: "admin", Path: "apps/admin", Folder: "/admin"},
			},
		}
		vars := map[string]map[string]string{
			"storefront": {"POSTHOG_ID": "ph-store"},
			"admin":      {"POSTHOG_ID": "ph-admin"},
		}
		if err := builder.Build(context.Background(), cfg, vars, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if len(got.Apps) != 2 {
			t.Fatalf("request included %d apps, want both", len(got.Apps))
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
		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, request []byte, _ Log) error {
			writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"))
			return json.Unmarshal(request, &got)
		}}

		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "web", Path: "apps/web", Folder: "/web"},
				{Name: "admin", Path: "apps/admin", Folder: "/admin"},
			},
		}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
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

		for _, name := range []string{"PATH", "NEXT_ADAPTER_PATH", constants.AppFolderEnvName} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeBuilder(t, root)

				ran := false
				builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, _ []byte, _ Log) error {
					ran = true
					return nil
				}}

				cfg := &projectconfig.Config{
					Dir:  root,
					Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}},
				}
				vars := map[string]map[string]string{"web": {name: "hijacked"}}
				err := builder.Build(context.Background(), cfg, vars, Log{})
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
		t.Setenv(constants.AppFolderEnvName, "/stale")

		var got []string
		builder := nodeOnly{node: func(_ context.Context, _ string, env []string, _ []byte, _ Log) error {
			got = env
			writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"))
			return nil
		}}

		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "web", Path: "apps/web", Folder: "/web"}},
		}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		value, found := lookup(got, constants.AppFolderEnvName)
		if !found {
			t.Fatal("no binding was stated, so a stale one from the parent environment still answers")
		}
		if value != "" {
			t.Errorf("%s = %q, want the project root", constants.AppFolderEnvName, value)
		}
	})

	t.Run("a planned bundle is produced from Go", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		entrypoint := filepath.Join(root, "apps", "api", "src", "server.js")
		if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entrypoint, []byte("export default { fetch: () => new Response('hi') };\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, _ []byte, _ Log) error {
			writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"), functionSummary{
				Name:         "api",
				Framework:    appbuild.Framework{Name: "node"},
				Handler:      "index.mjs",
				ArtifactPath: filepath.Join("apps", "api", "functions", "index.func"),
				Strategy:     bundleStrategy,
				Entrypoint:   entrypoint,
			})
			return nil
		}}

		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}},
		}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []manifestbuilder.Function{
			{Route: "index", Framework: manifestbuilder.Framework{Name: "node"}, Handler: "index.mjs", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})

		bundle := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", functionsDirName, "index.func", "index.mjs")
		if _, err := os.Stat(bundle); err != nil {
			t.Errorf("stat %s: %v (the plan asked Go to bundle)", bundle, err)
		}
		if got, err := BuildID(root, "api"); err != nil || len(got) != 16 {
			t.Errorf("BuildID = %q, %v, want the artifact hash the bundle wrote", got, err)
		}
	})

	t.Run("a planned trace leaves the tree the node builder wrote alone", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)

		builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, _ []byte, _ Log) error {
			outDir := filepath.Join(root, constants.ProjectStateDirName, "output")
			writeFuncConfig(t, outDir, "web", "index.func",
				appbuild.FunctionConfig{Framework: appbuild.Framework{Name: "next"}, Handler: "server.js", App: "web"})
			writePlan(t, outDir, functionSummary{
				Name:         "web",
				Framework:    appbuild.Framework{Name: "next"},
				Handler:      "server.js",
				ArtifactPath: filepath.Join("apps", "web", "functions", "index.func"),
				Strategy:     traceStrategy,
			})
			return nil
		}}

		cfg := &projectconfig.Config{
			Dir:  root,
			Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}},
		}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		funcDir := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "web", functionsDirName, "index.func")
		entries, err := os.ReadDir(funcDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != appbuild.FunctionConfigFile {
			t.Errorf("function directory contains %d entries, want only the %s the node builder wrote", len(entries), appbuild.FunctionConfigFile)
		}
	})

	plans := []struct {
		name  string
		plan  func(t *testing.T, outDir string)
		wants []string
	}{
		{
			name:  "no plan at all",
			plan:  func(_ *testing.T, _ string) {},
			wants: []string{buildPlanFileName},
		},
		{
			name: "an unreadable plan",
			plan: func(t *testing.T, outDir string) {
				if err := os.WriteFile(filepath.Join(outDir, buildPlanFileName), []byte("not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: []string{"invalid build plan"},
		},
		{
			name: "a strategy this build does not know",
			plan: func(t *testing.T, outDir string) {
				writePlan(t, outDir, functionSummary{Name: "api", Framework: appbuild.Framework{Name: "node"}, ArtifactPath: "apps/api/functions/index.func", Strategy: "teleport"})
			},
			wants: []string{"teleport"},
		},
		{
			name: "a bundle with no entrypoint",
			plan: func(t *testing.T, outDir string) {
				writePlan(t, outDir, functionSummary{Name: "api", Framework: appbuild.Framework{Name: "node"}, ArtifactPath: "apps/api/functions/index.func", Strategy: bundleStrategy})
			},
			wants: []string{"entrypoint"},
		},
		{
			name: "a bundle aimed outside the app layout",
			plan: func(t *testing.T, outDir string) {
				writePlan(t, outDir, functionSummary{Name: "api", Framework: appbuild.Framework{Name: "node"}, ArtifactPath: "elsewhere/index.func", Strategy: bundleStrategy, Entrypoint: filepath.Join(outDir, "server.js")})
			},
			wants: []string{"elsewhere"},
		},
	}
	for _, tt := range plans {
		t.Run(tt.name+" fails the build", func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeBuilder(t, root)
			builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, _ []byte, _ Log) error {
				outDir := filepath.Join(root, constants.ProjectStateDirName, "output")
				if err := os.MkdirAll(outDir, 0o755); err != nil {
					return err
				}
				tt.plan(t, outDir)
				return nil
			}}

			cfg := &projectconfig.Config{
				Dir:  root,
				Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}},
			}
			err := builder.Build(context.Background(), cfg, nil, Log{})
			if err == nil {
				t.Fatal("Build succeeded, want the unusable build plan to fail the build")
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}

	t.Run("over real node, builds the fixture app it is configured with", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: spawns real node over the builder")
		}

		fixtureRoot := expressFixture(t)
		cfg := &projectconfig.Config{
			Dir:  fixtureRoot,
			Apps: []projectconfig.App{{Name: "api", Path: "."}},
		}

		var stderr bytes.Buffer
		if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, Log{Shared: &stderr}); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := ReadFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("CollectFunctions returned %d functions, want 1: %+v", len(fns), fns)
		}
		want := manifestbuilder.Function{
			Route:        "index",
			Framework:    manifestbuilder.Framework{Name: "node"},
			Handler:      "index.mjs",
			ArtifactPath: "apps/api/functions/index.func",
			RouteID:      "/",
			App:          "api",
		}
		if fns[0] != want {
			t.Errorf("function = %+v, want %+v", fns[0], want)
		}
		if got, err := BuildID(fixtureRoot, "api"); err != nil || len(got) != 16 {
			t.Errorf("BuildID = %q, %v, want the artifact hash the build wrote", got, err)
		}
	})

	t.Run("over real node, detects a single app from an unconfigured project", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: spawns real node over the builder")
		}

		fixtureRoot := expressFixture(t)

		var stderr bytes.Buffer
		if err := (nodeOnly{node: runNode}).Build(context.Background(), &projectconfig.Config{Dir: fixtureRoot}, nil, Log{Shared: &stderr}); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := ReadFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("CollectFunctions: %v", err)
		}
		if len(fns) != 1 {
			t.Fatalf("CollectFunctions returned %d functions, want 1: %+v", len(fns), fns)
		}
		if fns[0].Route != "index" || fns[0].Framework.Name != "node" {
			t.Errorf("detected function = %+v, want route index framework node", fns[0])
		}
		if fns[0].App != "express-app" {
			t.Errorf("detected function app = %q, want %q", fns[0].App, "express-app")
		}
		id, err := DeploymentID(fixtureRoot, fns[0].App)
		if err != nil {
			t.Fatalf("DeploymentID(%q): %v", fns[0].App, err)
		}
		if len(id) != 32 {
			t.Errorf("DeploymentID = %q, want the id the build minted", id)
		}
	})
}

func TestBuildLearnsTheEdge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cfg          func(root string) *projectconfig.Config
		wantKind     string
		wantDegraded []string
	}{
		{
			name: "a project naming no edge names none to the builder either",
			cfg: func(root string) *projectconfig.Config {
				return &projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}}
			},
			wantKind: "",
		},
		{
			name: "a project naming an edge builds for that edge, with its waivers",
			cfg: func(root string) *projectconfig.Config {
				return &projectconfig.Config{
					Dir:           root,
					Edge:          &projectconfig.EdgeDescriptor{ID: "cloudflare"},
					AllowDegraded: []string{"edge-middleware", "edge-runtime"},
					Apps:          []projectconfig.App{{Name: "web", Path: "apps/web"}},
				}
			},
			wantKind:     "cloudflare",
			wantDegraded: []string{"edge-middleware", "edge-runtime"},
		},
		{
			name: "a project on the API Gateway edge builds for that edge",
			cfg: func(root string) *projectconfig.Config {
				return &projectconfig.Config{
					Dir:           root,
					Edge:          &projectconfig.EdgeDescriptor{ID: "api-gateway"},
					AllowDegraded: []string{"edge-middleware"},
					Apps:          []projectconfig.App{{Name: "web", Path: "apps/web"}},
				}
			},
			wantKind:     "api-gateway",
			wantDegraded: []string{"edge-middleware"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeBuilder(t, root)

			var got builderRequest
			builder := nodeOnly{node: func(_ context.Context, _ string, _ []string, request []byte, _ Log) error {
				writePlan(t, filepath.Join(root, constants.ProjectStateDirName, "output"))
				return json.Unmarshal(request, &got)
			}}

			if err := builder.Build(context.Background(), tc.cfg(root), nil, Log{}); err != nil {
				t.Fatalf("Build: %v", err)
			}

			if got.EdgeKind != tc.wantKind {
				t.Errorf("edgeKind = %q, want %q", got.EdgeKind, tc.wantKind)
			}
			if !slices.Equal(got.AllowDegraded, tc.wantDegraded) {
				t.Errorf("allowDegraded = %v, want %v", got.AllowDegraded, tc.wantDegraded)
			}
		})
	}
}

type nodeOnly struct {
	node nodeRun
}

func (n nodeOnly) Build(ctx context.Context, cfg *projectconfig.Config, env map[string]map[string]string, log Log) error {
	return tools{node: n.node}.functions(ctx, cfg, env, log)
}
