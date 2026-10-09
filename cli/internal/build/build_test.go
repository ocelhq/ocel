package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/arch"
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

func writeFuncConfig(t *testing.T, outDir, app, funcRel string, cfg buildoutput.FunctionConfig) {
	t.Helper()
	dir := filepath.Join(outDir, "apps", app, buildoutput.FunctionsDir, funcRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.FunctionConfigFile), data, 0o644); err != nil {
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
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: app.Name})
			}
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{"POSTHOG_ID": "ph-web"}}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if gotScript != scriptPath {
			t.Errorf("script path = %q, want %q", gotScript, scriptPath)
		}
		if len(got.Apps) != 2 {
			t.Fatalf("request had %d apps, want 2", len(got.Apps))
		}
		outputDir := outputRoot(t, root)
		for i, want := range []nodeAppBuild{
			{Framework: "next", Name: "web", Cwd: filepath.Join(root, "apps/web"), OutputDir: buildoutput.AppRoot(outputDir, "web"), Folder: "/web", Env: map[string]string{"POSTHOG_ID": "ph-web"}},
			{Framework: "next", Name: "docs", Cwd: filepath.Join(root, "apps/docs"), OutputDir: buildoutput.AppRoot(outputDir, "docs")},
		} {
			app := got.Apps[i]
			recorded, err := BuildID(root, want.Name)
			if err != nil {
				t.Fatalf("BuildID(%s): %v", want.Name, err)
			}
			if app.BuildID != recorded {
				t.Errorf("%s builds under build id %q, want its recorded %q", want.Name, app.BuildID, recorded)
			}
			if app.Framework != want.Framework || app.Name != want.Name || app.Cwd != want.Cwd || app.OutputDir != want.OutputDir || app.Folder != want.Folder || app.Entrypoint != "" || app.FunctionDir != "" {
				t.Errorf("app[%d] = %+v, want %+v", i, app, want)
			}
			if app.Env["POSTHOG_ID"] != want.Env["POSTHOG_ID"] {
				t.Errorf("%s POSTHOG_ID = %q, want %q", want.Name, app.Env["POSTHOG_ID"], want.Env["POSTHOG_ID"])
			}
		}
		if got.Apps[0].BuildID == got.Apps[1].BuildID {
			t.Errorf("both apps build under %q, want an id each", got.Apps[0].BuildID)
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

	t.Run("hands a sveltekit app to the node build script with what its build needs", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		shop := project.App{Name: "shop", Path: "apps/shop", Folder: "/shop", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: buildoutput.FrameworkSvelteKit}}
		cfg := &project.Project{Dir: root, Apps: []project.App{shop}}

		var got nodeBuildRequest
		builder := nodeOnly{node: requestOf(&got)}
		if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"shop": {Env: map[string]string{"PUBLIC_KEY": "pk"}}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if len(got.Apps) != 1 {
			t.Fatalf("request had %d apps, want 1", len(got.Apps))
		}
		app := got.Apps[0]
		recorded, err := BuildID(root, "shop")
		if err != nil {
			t.Fatalf("BuildID: %v", err)
		}
		want := nodeAppBuild{Framework: "sveltekit", Name: "shop", Cwd: filepath.Join(root, "apps/shop"), OutputDir: buildoutput.AppRoot(outputRoot(t, root), "shop"), BuildID: recorded, Folder: "/shop"}
		if app.Framework != want.Framework || app.Name != want.Name || app.Cwd != want.Cwd || app.OutputDir != want.OutputDir || app.BuildID != want.BuildID || app.Folder != want.Folder {
			t.Errorf("app = %+v, want %+v", app, want)
		}
		if app.Env["PUBLIC_KEY"] != "pk" {
			t.Errorf("the build reads PUBLIC_KEY=%q, want the value the app resolves", app.Env["PUBLIC_KEY"])
		}
	})

	t.Run("hands each next app the size budget its host sets for one function", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var got nodeBuildRequest
		builder := nodeOnly{node: requestOf(&got), host: Host{MaxFunctionBytes: 200 << 20}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if len(got.Apps) != 1 || got.Apps[0].MaxFunctionBytes != 200<<20 {
			t.Errorf("request apps = %+v, want web packed against its host's size budget", got.Apps)
		}
	})

	t.Run("installs sharp's linux build once for every bundle of a next app", func(t *testing.T) {
		runs := installLinuxArm64SharpNpm(t)

		root := t.TempDir()
		writeBuildScript(t, root)
		web := nextApp("web", "apps/web")
		web.Arch = "arm64"
		cfg := &project.Project{Dir: root, Apps: []project.App{web}}

		var functionDirs []string
		builder := nodeOnly{node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			out := got.Apps[0].OutputDir
			for _, bundle := range []string{"bundle-0.func", "bundle-1.func"} {
				writeFuncConfig(t, filepath.Dir(filepath.Dir(out)), "web", bundle,
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
				functionDir := filepath.Join(out, buildoutput.FunctionsDir, bundle)
				writeDarwinSharp(t, functionDir)
				functionDirs = append(functionDirs, functionDir)
			}
			return nil
		}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		recorded, err := os.ReadFile(runs)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(recorded), "run"); got != 1 {
			t.Errorf("npm ran %d times for two bundles tracing the same sharp, want once", got)
		}
		for _, functionDir := range functionDirs {
			assertLinuxArm64Sharp(t, functionDir)
		}
	})

	t.Run("ships each next function sharp built for the linux architecture its app declares", func(t *testing.T) {
		installLinuxArm64SharpNpm(t)

		root := t.TempDir()
		writeBuildScript(t, root)
		web := nextApp("web", "apps/web")
		web.Arch = "arm64"
		cfg := &project.Project{Dir: root, Apps: []project.App{web}}

		var functionDir string
		builder := nodeOnly{node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			out := got.Apps[0].OutputDir
			writeFuncConfig(t, filepath.Dir(filepath.Dir(out)), "web", "bundle-0.func",
				buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
			functionDir = filepath.Join(out, buildoutput.FunctionsDir, "bundle-0.func")
			writeDarwinSharp(t, functionDir)
			return nil
		}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		assertLinuxArm64Sharp(t, functionDir)
	})

	t.Run("refuses a next function that goes over its host's size budget once the target's variants are installed", func(t *testing.T) {
		installLinuxArm64SharpNpmWeighing(t, 1000)

		root := t.TempDir()
		writeBuildScript(t, root)
		web := nextApp("web", "apps/web")
		web.Arch = "arm64"
		cfg := &project.Project{Dir: root, Apps: []project.App{web}}

		builder := nodeOnly{host: Host{MaxFunctionBytes: 2000}, node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			out := got.Apps[0].OutputDir
			writeFuncConfig(t, filepath.Dir(filepath.Dir(out)), "web", "bundle-0.func",
				buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
			functionDir := filepath.Join(out, buildoutput.FunctionsDir, "bundle-0.func")
			writeDarwinSharp(t, functionDir)
			writeFile(t, filepath.Join(functionDir, "server.js"), strings.Repeat("x", 1500))
			return nil
		}}
		err := builder.Build(context.Background(), cfg, nil, Log{})
		if err == nil {
			t.Fatal("Build = nil error, want the function refused: it fits its budget before the linux sharp is installed and not after")
		}
		for _, want := range []string{"bundle-0.func", "2000"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Build error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("ships a next function that fits its host's size budget once the target's variants are installed", func(t *testing.T) {
		installLinuxArm64SharpNpmWeighing(t, 1000)

		root := t.TempDir()
		writeBuildScript(t, root)
		web := nextApp("web", "apps/web")
		web.Arch = "arm64"
		cfg := &project.Project{Dir: root, Apps: []project.App{web}}

		builder := nodeOnly{host: Host{MaxFunctionBytes: 1 << 20}, node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			out := got.Apps[0].OutputDir
			writeFuncConfig(t, filepath.Dir(filepath.Dir(out)), "web", "bundle-0.func",
				buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
			writeDarwinSharp(t, filepath.Join(out, buildoutput.FunctionsDir, "bundle-0.func"))
			return nil
		}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}
	})

	t.Run("refuses a serverless next app that names its own adapter before next build runs", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		writeNextConfig(t, root, "web", "next.config.mjs", `export default { adapterPath: "./a.mjs" }`)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "web")}}
		ran := false

		err := nodeOnly{node: func(context.Context, string, []byte, Log) error {
			ran = true
			return nil
		}}.Build(context.Background(), cfg, nil, Log{})
		if err == nil || !strings.Contains(err.Error(), "sets adapterPath in next.config.mjs") {
			t.Errorf("Build() = %v, want a refusal naming adapterPath in next.config.mjs", err)
		}
		if ran {
			t.Error("Build() ran the node build for an app whose adapter ocel cannot run")
		}
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
		writeFuncConfig(t, outputRoot(t, root), "stale", "index.func",
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "node"}, EntryFile: "h", App: "stale"})

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
		if _, err := os.Stat(filepath.Join(buildoutput.AppsRoot(outputRoot(t, root)), "stale")); !errors.Is(err, os.ErrNotExist) {
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

	t.Run("refuses a value under a name the build sets or, sensitive or secret, one its toolchain reads from its environment", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name   string
			values AppVariables
		}{
			{"PATH", AppVariables{Env: map[string]string{"PATH": "hijacked"}}},
			{processenv.AppFolderEnvVar, AppVariables{Env: map[string]string{processenv.AppFolderEnvVar: "hijacked"}}},
			{processenv.PhaseEnvVar, AppVariables{Env: map[string]string{processenv.PhaseEnvVar: "hijacked"}}},
			{processenv.LiveDirEnvVar, AppVariables{Env: map[string]string{processenv.LiveDirEnvVar: "hijacked"}}},
			{"NODE_ENV", AppVariables{Env: map[string]string{"NODE_ENV": "development"}}},
			{"OCEL_APP_NAME", AppVariables{Env: map[string]string{"OCEL_APP_NAME": "hijacked"}}},
			{"NEXT_DEPLOYMENT_ID", AppVariables{Live: map[string]string{"NEXT_DEPLOYMENT_ID": "hijacked"}}},
			{"NEXT_ADAPTER_PATH", AppVariables{Env: map[string]string{"NEXT_ADAPTER_PATH": "hijacked"}}},
			{"HOME", AppVariables{Live: map[string]string{"HOME": "/secret/home"}}},
			{"TMPDIR", AppVariables{Live: map[string]string{"TMPDIR": "/secret/tmp"}}},
			{"NODE_OPTIONS", AppVariables{Live: map[string]string{"NODE_OPTIONS": "--require=x"}}},
			{"CARGO_REGISTRY_TOKEN", AppVariables{Live: map[string]string{"CARGO_REGISTRY_TOKEN": "cio_secret"}}},
			{"RUSTUP_HOME", AppVariables{Live: map[string]string{"RUSTUP_HOME": "/secret/rustup"}}},
			{"npm_config__authToken", AppVariables{Live: map[string]string{"npm_config__authToken": "npm_secret"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeBuildScript(t, root)
				ran := false
				builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
					ran = true
					return nil
				}}

				cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
				err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": tc.values}, Log{})
				if err == nil || !strings.Contains(err.Error(), tc.name) || !strings.Contains(err.Error(), `app "web"`) {
					t.Errorf("Build err = %v, want a refusal naming app \"web\" and %q", err, tc.name)
				}
				if ran {
					t.Error("the node build script ran, want the refusal before anything is built")
				}
			})
		}
	})

	t.Run("builds with a plaintext value under a name its toolchain reads, which the build hands it in place of the shell's", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
		var got nodeBuildRequest
		builder := nodeOnly{node: requestOf(&got)}
		if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{"NODE_OPTIONS": "--max-old-space-size=4096"}}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got.Apps[0].Env["NODE_OPTIONS"] != "--max-old-space-size=4096" {
			t.Errorf("Env[NODE_OPTIONS] = %q, want the plaintext value", got.Apps[0].Env["NODE_OPTIONS"])
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
			{Route: "index", Framework: buildoutput.Framework{Name: "node", Arch: arch.X8664}, EntryFile: "index.mjs", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		if got, err := FrameworkBuildID(root, "api"); err != nil || len(got) != 16 {
			t.Errorf("FrameworkBuildID = %q, %v, want the artifact hash the bundle wrote", got, err)
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
		if err == nil || !strings.Contains(err.Error(), "compute: { serverless: { framework: … } }") {
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
			{Route: "index", Framework: buildoutput.Framework{Name: "node", Arch: arch.X8664}, EntryFile: "index.mjs", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
	})

}

func installLinuxArm64SharpNpm(t *testing.T) (runs string) {
	t.Helper()
	return installLinuxArm64SharpNpmWeighing(t, 0)
}

func installLinuxArm64SharpNpmWeighing(t *testing.T, ballastBytes int) (runs string) {
	t.Helper()
	bin := t.TempDir()
	runs = filepath.Join(t.TempDir(), "runs")
	npm := "#!/bin/sh\necho run >> '" + runs + "'\nmkdir -p node_modules/sharp node_modules/@img/sharp-linux-arm64\n" +
		"head -c " + strconv.Itoa(ballastBytes) + " /dev/zero > node_modules/@img/sharp-linux-arm64/libvips.node\n" +
		"printf '%s' '{\"name\":\"sharp\",\"version\":\"0.34.5\"}' > node_modules/sharp/package.json\n" +
		"printf '%s' '{\"name\":\"@img/sharp-linux-arm64\",\"version\":\"0.34.5\",\"os\":[\"linux\"],\"cpu\":[\"arm64\"],\"libc\":[\"glibc\"]}' > node_modules/@img/sharp-linux-arm64/package.json\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return runs
}

func writeDarwinSharp(t *testing.T, functionDir string) {
	t.Helper()
	writeFile(t, filepath.Join(functionDir, "node_modules", "sharp", "package.json"),
		`{"name":"sharp","version":"0.34.5","optionalDependencies":{"@img/sharp-darwin-arm64":"0.34.5","@img/sharp-linux-arm64":"0.34.5"}}`)
	writeFile(t, filepath.Join(functionDir, "node_modules", "@img", "sharp-darwin-arm64", "package.json"),
		`{"name":"@img/sharp-darwin-arm64","version":"0.34.5","os":["darwin"],"cpu":["arm64"]}`)
}

func assertLinuxArm64Sharp(t *testing.T, functionDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(functionDir, "node_modules", "@img", "sharp-linux-arm64", "package.json")); err != nil {
		t.Errorf("the function holds no sharp build for linux/arm64: %v", err)
	}
	if _, err := os.Stat(filepath.Join(functionDir, "node_modules", "@img", "sharp-darwin-arm64")); err == nil {
		t.Error("the function still ships the build host's darwin sharp")
	}
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
			writeFile(t, filepath.Join(got.Apps[0].FunctionDir, "src", "server.js"), "export default {};\n")
			return nil
		}}
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"}}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		functionDir := filepath.Join(buildoutput.AppRoot(outputRoot(t, root), "api"), buildoutput.FunctionsDir, buildoutput.RootFunctionDir)
		want := nodeAppBuild{Framework: "node", Name: "api", Cwd: source, Entrypoint: filepath.Join(source, "src", "server.ts"), FunctionDir: functionDir}
		if len(got.Apps) != 1 || got.Apps[0].Framework != want.Framework || got.Apps[0].Name != want.Name || got.Apps[0].Cwd != want.Cwd || got.Apps[0].Entrypoint != want.Entrypoint || got.Apps[0].FunctionDir != want.FunctionDir {
			t.Fatalf("request apps = %+v, want [%+v]", got.Apps, want)
		}

		fns, err := ReadFunctions(root)
		if err != nil {
			t.Fatalf("ReadFunctions: %v", err)
		}
		assertFunctions(t, "ReadFunctions", fns, []Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node", Arch: "arm64"}, EntryFile: "src/server.js", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		hosting, found, err := buildoutput.ReadHosting(outputRoot(t, root), "api")
		if err != nil || !found {
			t.Fatalf("ReadHosting = %v, %v", found, err)
		}
		if hosting.Framework != "node" || len(hosting.FrameworkBuildID) != 16 || hosting.RootFunction != "/" || hosting.Needs == nil || hosting.RouteTable != "" || hosting.Version != buildoutput.HostingVersion {
			t.Errorf("hosting.json = %+v, want a node app's hosting", hosting)
		}
		if _, err := os.Stat(filepath.Join(functionDir, buildoutput.HostingFile)); err == nil {
			t.Error("hosting.json landed inside the function directory")
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

	t.Run("ships the traced function sharp built for the linux architecture its app declares", func(t *testing.T) {
		installLinuxArm64SharpNpm(t)
		root := t.TempDir()
		writeBuildScript(t, root)
		writeFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export default {};\n")

		var functionDir string
		builder := nodeOnly{node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			functionDir = got.Apps[0].FunctionDir
			writeFile(t, filepath.Join(functionDir, "src", "server.js"), "export default {};\n")
			writeDarwinSharp(t, functionDir)
			return nil
		}}
		cfg := &project.Project{Dir: root, Apps: []project.App{{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"}}}
		if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		assertLinuxArm64Sharp(t, functionDir)
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
			{Route: "index", Framework: buildoutput.Framework{Name: "node", Arch: arch.X8664}, EntryFile: "src/server.js", ArtifactPath: "apps/api/functions/index.func", RouteID: "/", App: "api"},
		})
		functionDir := filepath.Join(buildoutput.AppRoot(outputRoot(t, fixtureRoot), "api"), buildoutput.FunctionsDir, buildoutput.RootFunctionDir)
		for _, rel := range []string{"src/server.js", "node_modules/express/package.json"} {
			if _, err := os.Stat(filepath.Join(functionDir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("traced artifact lacks %s: %v", rel, err)
			}
		}
		if got, err := FrameworkBuildID(fixtureRoot, "api"); err != nil || len(got) != 16 {
			t.Errorf("FrameworkBuildID = %q, %v, want the artifact hash", got, err)
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
	host Host
}

func (n nodeOnly) Build(ctx context.Context, cfg *project.Project, variables map[string]AppVariables, log Log) error {
	return tools{node: n.node}.functions(ctx, cfg, variables, n.host, log)
}

func outputRoot(t *testing.T, projectDir string) string {
	t.Helper()
	root, err := buildoutput.Root(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
