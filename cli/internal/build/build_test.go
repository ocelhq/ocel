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

	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func writeBuildScript(t *testing.T, projectDir string) string {
	t.Helper()
	path := node.BuildScriptPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFuncConfig(t *testing.T, outDir, app, funcRel string, cfg buildoutput.FunctionDescriptor) {
	t.Helper()
	dir := filepath.Join(outDir, "apps", app, functionsDirName, funcRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.FunctionDescriptorFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFunctions(t *testing.T, label string, got, want []Function) {
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
	fixtureRoot := filepath.Join(fixturetest.RepoDir(t), "frameworks", "node", "build", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, statedir.Name)) })
	if err := node.Ensure(fixtureRoot); err != nil {
		t.Fatalf("node.Ensure: %v", err)
	}
	return fixtureRoot
}

func nextApp(name, path string) project.App {
	return project.App{Name: name, Path: path, Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}}
}

func requestOf(got *nodeBuildRequest) nodeRun {
	return func(_ context.Context, _ string, request []byte, _ Log) error {
		return json.Unmarshal(request, got)
	}
}

func TestBuild(t *testing.T) {
	t.Run("hands each next app to the node build script with what its build needs", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		scriptPath := writeBuildScript(t, root)
		web := nextApp("web", "apps/web")
		web.Folder = "/web"
		cfg := &project.Project{Dir: root, Apps: []project.App{web, nextApp("docs", "apps/docs")}}

		var gotScript string
		var got nodeBuildRequest
		builder := nodeOnly{node: func(_ context.Context, script string, request []byte, _ Log) error {
			gotScript = script
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			for _, app := range got.Apps {
				writeFuncConfig(t, filepath.Dir(filepath.Dir(app.OutputDir)), app.Name, "index.func",
					buildoutput.FunctionDescriptor{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: app.Name})
			}
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, map[string]map[string]string{"web": {"POSTHOG_ID": "ph-web"}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if gotScript != scriptPath {
			t.Errorf("script path = %q, want %q", gotScript, scriptPath)
		}
		if len(got.Apps) != 2 {
			t.Fatalf("request had %d apps, want 2", len(got.Apps))
		}
		outputDir := buildoutput.Root(root)
		for i, want := range []nodeAppBuild{
			{Framework: "next", Name: "web", Cwd: filepath.Join(root, "apps/web"), OutputDir: buildoutput.AppRoot(outputDir, "web"), Folder: "/web", Env: map[string]string{"POSTHOG_ID": "ph-web"}},
			{Framework: "next", Name: "docs", Cwd: filepath.Join(root, "apps/docs"), OutputDir: buildoutput.AppRoot(outputDir, "docs")},
		} {
			app := got.Apps[i]
			recorded, err := DeploymentID(root, want.Name)
			if err != nil {
				t.Fatalf("DeploymentID(%s): %v", want.Name, err)
			}
			if app.DeploymentID != recorded {
				t.Errorf("%s builds under deployment id %q, want its recorded %q", want.Name, app.DeploymentID, recorded)
			}
			if app.Framework != want.Framework || app.Name != want.Name || app.Cwd != want.Cwd || app.OutputDir != want.OutputDir || app.Folder != want.Folder || app.Entrypoint != "" || app.FuncDir != "" {
				t.Errorf("app[%d] = %+v, want %+v", i, app, want)
			}
			if app.Env["POSTHOG_ID"] != want.Env["POSTHOG_ID"] {
				t.Errorf("%s POSTHOG_ID = %q, want %q", want.Name, app.Env["POSTHOG_ID"], want.Env["POSTHOG_ID"])
			}
		}
		if got.Apps[0].DeploymentID == got.Apps[1].DeploymentID {
			t.Errorf("both apps build under %q, want an id each", got.Apps[0].DeploymentID)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/docs/functions/index.func", App: "docs"},
			{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/index.func", App: "web"},
		})
	})

	t.Run("names the missing build script when none was materialized", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		err := nodeOnly{node: runNode}.Build(context.Background(), cfg, nil, Log{})
		if err == nil {
			t.Fatal("Build succeeded with no materialized build script, want error")
		}
		if !strings.Contains(err.Error(), node.BuildScriptPath(root)) {
			t.Errorf("error = %q, want it to name the missing build script", err)
		}
	})

	t.Run("with no apps and no package.json at the root resets output and builds nothing", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFuncConfig(t, buildoutput.Root(root), "stale", "index.func",
			buildoutput.FunctionDescriptor{Framework: buildoutput.Framework{Name: "node"}, EntryFile: "h", App: "stale"})

		ran := false
		builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
			ran = true
			return nil
		}}
		if err := builder.Build(context.Background(), &project.Project{Dir: root}, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if ran {
			t.Error("the node build script ran for a project with nothing to build")
		}
		if _, err := os.Stat(filepath.Join(buildoutput.AppsRoot(buildoutput.Root(root)), "stale")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale .func survived the reset (stat err = %v)", err)
		}
	})

	t.Run("a build failure returns a clear error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
			return errors.New("node build failed: web has no build script")
		}}

		err := builder.Build(context.Background(), cfg, nil, Log{})
		if err == nil || !strings.Contains(err.Error(), "no build script") {
			t.Errorf("error = %v, want it to surface the node build's failure", err)
		}
	})

	t.Run("refuses a resolved value the build environment owns", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"PATH", processenv.AppFolderEnvVar, processenv.PhaseEnvVar} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeBuildScript(t, root)
				ran := false
				builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
					ran = true
					return nil
				}}

				cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
				err := builder.Build(context.Background(), cfg, map[string]map[string]string{"web": {name: "hijacked"}}, Log{})
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Errorf("Build err = %v, want a refusal naming %q", err, name)
				}
				if ran {
					t.Error("the node build script ran, want the refusal before anything is built")
				}
			})
		}
	})

	t.Run("a node app is bundled here, without the node build script", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "apps", "api", "src", "server.js"), "export default { fetch: () => new Response('hi') };\n")

		ran := false
		builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
			ran = true
			return nil
		}}
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}}}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if ran {
			t.Error("the node build script ran for an app bundled here")
		}
		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.mjs", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		if got, err := BuildID(root, "api"); err != nil || len(got) != 16 {
			t.Errorf("BuildID = %q, %v, want the artifact hash the bundle wrote", got, err)
		}
	})

	t.Run("a node app with no entrypoint names the files it looked for", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}}}}
		err := nodeOnly{node: runNode}.Build(context.Background(), cfg, nil, Log{})
		if err == nil || !strings.Contains(err.Error(), "src/server.ts") || !strings.Contains(err.Error(), `"api"`) {
			t.Errorf("Build err = %v, want the app and the entrypoints tried named", err)
		}
	})

	t.Run("an app that states no framework is refused", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless}}}
		err := nodeOnly{node: runNode}.Build(context.Background(), cfg, nil, Log{})
		if err == nil || !strings.Contains(err.Error(), `"framework"`) {
			t.Errorf("Build err = %v, want the app told to state its framework", err)
		}
	})

	t.Run("over real node, bundles the fixture app it is configured with", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: bundles a real app")
		}

		fixtureRoot := expressFixture(t)
		cfg := &project.Project{Dir: fixtureRoot, Apps: []project.App{{Name: "api", Path: ".", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}}}}

		var stderr bytes.Buffer
		if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, Log{Shared: &stderr}); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := ReadFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.mjs", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
	})

}

func TestBuildTracesANodeAppWhenTracingIsPreferred(t *testing.T) {
	t.Setenv(toolchain.PreferTracingEnv, "1")

	t.Run("the node build script copies the sources, and the artifact is described here", func(t *testing.T) {
		root := t.TempDir()
		writeBuildScript(t, root)
		source := filepath.Join(root, "apps", "api")
		writeFile(t, filepath.Join(source, "src", "server.ts"), "export default {};\n")

		var got nodeBuildRequest
		builder := nodeOnly{node: func(_ context.Context, _ string, request []byte, _ Log) error {
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			writeFile(t, filepath.Join(got.Apps[0].FuncDir, "src", "server.js"), "export default {};\n")
			return nil
		}}
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"}}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		funcDir := filepath.Join(buildoutput.AppRoot(buildoutput.Root(root), "api"), functionsDirName, entryFuncDirName)
		want := nodeAppBuild{Framework: "node", Name: "api", Cwd: source, Entrypoint: filepath.Join(source, "src", "server.ts"), FuncDir: funcDir}
		if len(got.Apps) != 1 || got.Apps[0].Framework != want.Framework || got.Apps[0].Name != want.Name || got.Apps[0].Cwd != want.Cwd || got.Apps[0].Entrypoint != want.Entrypoint || got.Apps[0].FuncDir != want.FuncDir {
			t.Fatalf("request apps = %+v, want [%+v]", got.Apps, want)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node", Arch: "arm64"}, EntryFile: "src/server.js", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		desc, found, err := buildoutput.ReadServeDescriptor(buildoutput.Root(root), "api")
		if err != nil || !found {
			t.Fatalf("ReadServeDescriptor = %v, %v", found, err)
		}
		if desc.Framework != "node" || len(desc.BuildID) != 16 || desc.Entry != "/" || desc.Needs == nil || desc.EdgeRouting {
			t.Errorf("serve descriptor = %+v, want a node app's descriptor", desc)
		}
		if _, err := os.Stat(filepath.Join(funcDir, edge.ServeDescriptorFile)); err == nil {
			t.Error("the serve descriptor landed inside the function directory")
		}
	})

	t.Run("an entrypoint outside the app's own sources is refused before the node build script runs", func(t *testing.T) {
		root := t.TempDir()
		writeBuildScript(t, root)
		writeFile(t, filepath.Join(root, "shared", "server.js"), "export default {};\n")

		ran := false
		builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
			ran = true
			return nil
		}}
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node", Entrypoint: "../../shared/server.js"}}}}
		err := builder.Build(context.Background(), cfg, nil, Log{})
		if err == nil || !strings.Contains(err.Error(), toolchain.PreferTracingEnv) {
			t.Errorf("Build err = %v, want a refusal that names how to bundle instead", err)
		}
		if ran {
			t.Error("the node build script ran for an entrypoint it cannot serve")
		}
	})

	t.Run("over real node, traces the fixture app into its own module tree", func(t *testing.T) {
		if testing.Short() {
			t.Skip("integration test: spawns real node over the build script")
		}

		fixtureRoot := expressFixture(t)
		cfg := &project.Project{Dir: fixtureRoot, Apps: []project.App{{Name: "api", Path: ".", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}}}}

		var stderr bytes.Buffer
		if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, Log{Shared: &stderr}); err != nil {
			t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
		}

		fns, err := ReadFunctions(fixtureRoot)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		funcDir := filepath.Join(buildoutput.AppRoot(buildoutput.Root(fixtureRoot), "api"), functionsDirName, entryFuncDirName)
		for _, rel := range []string{"src/server.js", "node_modules/express/package.json"} {
			if _, err := os.Stat(filepath.Join(funcDir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("traced artifact lacks %s: %v", rel, err)
			}
		}
		if got, err := BuildID(fixtureRoot, "api"); err != nil || len(got) != 16 {
			t.Errorf("BuildID = %q, %v, want the artifact hash", got, err)
		}
	})
}

func TestBuildLearnsTheEdge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cfg          func(root string) *project.Project
		wantKind     string
		wantDegraded []string
	}{
		{
			name: "a project naming no edge names none to the build either",
			cfg: func(root string) *project.Project {
				return &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
			},
			wantKind: "",
		},
		{
			name: "a project naming an edge builds for that edge, with its waivers",
			cfg: func(root string) *project.Project {
				return &project.Project{
					Dir:           root,
					Edge:          &project.Edge{Kind: "relay"},
					AllowDegraded: []edge.Need{edge.NeedEdgeMiddleware, edge.NeedEdgeRuntime},
					Apps:          []project.App{nextApp("web", "apps/web")},
				}
			},
			wantKind:     "relay",
			wantDegraded: []string{"edge-middleware", "edge-runtime"},
		},
		{
			name: "a project on another edge builds for that edge, with only its own waivers",
			cfg: func(root string) *project.Project {
				return &project.Project{
					Dir:           root,
					Edge:          &project.Edge{Kind: "direct"},
					AllowDegraded: []edge.Need{edge.NeedEdgeMiddleware},
					Apps:          []project.App{nextApp("web", "apps/web")},
				}
			},
			wantKind:     "direct",
			wantDegraded: []string{"edge-middleware"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeBuildScript(t, root)

			var got nodeBuildRequest
			if err := (nodeOnly{node: requestOf(&got)}).Build(context.Background(), tc.cfg(root), nil, Log{}); err != nil {
				t.Fatalf("Build: %v", err)
			}

			if len(got.Apps) != 1 {
				t.Fatalf("request had %d apps, want 1", len(got.Apps))
			}
			if got.Apps[0].EdgeKind != tc.wantKind {
				t.Errorf("edgeKind = %q, want %q", got.Apps[0].EdgeKind, tc.wantKind)
			}
			if !slices.Equal(got.Apps[0].AllowDegraded, tc.wantDegraded) {
				t.Errorf("allowDegraded = %v, want %v", got.Apps[0].AllowDegraded, tc.wantDegraded)
			}
		})
	}
}

type nodeOnly struct {
	node nodeRun
}

func (n nodeOnly) Build(ctx context.Context, cfg *project.Project, env map[string]map[string]string, log Log) error {
	return tools{node: n.node}.functions(ctx, cfg, env, log)
}
