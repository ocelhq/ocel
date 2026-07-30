package appbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/platform"
)

func swapExec(t *testing.T, fn func(ctx context.Context, scriptPath string, env []string, request []byte, stderr io.Writer) error) {
	t.Helper()
	prev := builderExec
	builderExec = fn
	t.Cleanup(func() { builderExec = prev })
}

// lookup answers what the spawned process would see for name: exec is
// last-wins, so the last entry is the one that counts.
func lookup(env []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		if rest, ok := strings.CutPrefix(entry, name+"="); ok {
			value, found = rest, true
		}
	}
	return value, found
}

// writeBuilder puts a stub builder where platform.Ensure would have
// materialized the real one, so Build's existence check passes.
func writeBuilder(t *testing.T, projectDir string) string {
	t.Helper()
	path := platform.BuilderPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFuncConfig simulates one thing the builder does: writing a `.func`
// directory with its config.json into the app's own subtree of the output.
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

func TestBuild_RunsBuilderAndDiscoversFunctions(t *testing.T) {
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
	swapExec(t, func(_ context.Context, scriptPath string, env []string, request []byte, _ io.Writer) error {
		gotScript = scriptPath
		gotEnv = env
		if err := json.Unmarshal(request, &gotReq); err != nil {
			return err
		}
		// Simulate the builder writing its output tree.
		writeFuncConfig(t, gotReq.OutDir, "api", "index.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "api"})
		writeFuncConfig(t, gotReq.OutDir, "worker", "index.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "worker"})
		return nil
	})

	if err := Build(context.Background(), cfg, nil, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}

	fns, err := CollectFunctions(root)
	if err != nil {
		t.Fatalf("CollectFunctions: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "api/index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/api/functions/index.func", Framework: "express", App: "api"},
		{Name: "worker/index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/worker/functions/index.func", Framework: "express", App: "worker"},
	}
	if len(fns) != len(want) {
		t.Fatalf("CollectFunctions returned %d functions, want %d: %+v", len(fns), len(want), fns)
	}
	for i, w := range want {
		if fns[i] != w {
			t.Errorf("function[%d] = %+v, want %+v", i, fns[i], w)
		}
	}

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

	// Both artifacts are resolved from the project's materialized platform dist.
	if gotScript != builderPath {
		t.Errorf("script path = %q, want %q", gotScript, builderPath)
	}
	if got, _ := lookup(gotEnv, "NEXT_ADAPTER_PATH"); got != platform.AdapterPath(root) {
		t.Errorf("adapter path = %q, want %q", got, platform.AdapterPath(root))
	}
}

func TestBuild_MissingBuilder(t *testing.T) {
	root := t.TempDir()
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	err := Build(context.Background(), cfg, nil, io.Discard)
	if err == nil {
		t.Fatal("Build succeeded with no materialized builder, want error")
	}
	if !strings.Contains(err.Error(), platform.BuilderPath(root)) {
		t.Errorf("error = %q, want it to name the missing builder path", err)
	}
}

func TestBuild_NoApps_RunsBuilderForDetectionAndResetsOutput(t *testing.T) {
	root := t.TempDir()
	writeBuilder(t, root)
	// A stale artifact from a previous build must not survive to be deployed.
	writeFuncConfig(t, filepath.Join(root, ".ocel", "output"), "stale", "index.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "h", Framework: "express", App: "stale"})

	var gotReq builderRequest
	swapExec(t, func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
		// Simulate the builder running detection and finding nothing to build.
		return json.Unmarshal(request, &gotReq)
	})

	if err := Build(context.Background(), &projectconfig.Config{Dir: root}, nil, io.Discard); err != nil {
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
}

func TestBuild_BuildFailure_ReturnsClearError(t *testing.T) {
	root := t.TempDir()
	writeBuilder(t, root)
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	swapExec(t, func(_ context.Context, _ string, _ []string, _ []byte, _ io.Writer) error {
		return errors.New("node-builder failed: no entrypoint resolved for app \"api\"")
	})

	err := Build(context.Background(), cfg, nil, io.Discard)
	if err == nil {
		t.Fatal("Build succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no entrypoint resolved") {
		t.Errorf("error = %q, want it to surface the node-builder failure", err)
	}
}

func TestCollect_NoOutputTree_Errors(t *testing.T) {
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
}

// A built tree that produced no functions — a fully static export, say — is a
// fact about the project, not a failure.
func TestCollect_EmptyOutputTree_NoError(t *testing.T) {
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
}

func TestCollect_ReadsPrebuiltTree(t *testing.T) {
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

	want := []manifestbuilder.Function{
		{Name: "web/api/todos/[id]", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", Framework: "next", App: "web"},
		{Name: "web/index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/index.func", Framework: "next", App: "web"},
	}
	if len(fns) != len(want) {
		t.Fatalf("CollectFunctions returned %d functions, want %d: %+v", len(fns), len(want), fns)
	}
	for i, w := range want {
		if fns[i] != w {
			t.Errorf("function[%d] = %+v, want %+v", i, fns[i], w)
		}
	}
}

func TestCollectFunctions_Nested(t *testing.T) {
	outDir := t.TempDir()
	writeFuncConfig(t, outDir, "web", filepath.Join("api", "todos", "[id].func"),
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
	writeFuncConfig(t, outDir, "web", "index.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
	// A nested node_modules with its own package.json must not be mistaken for
	// a function (no config.json, and it lives inside a .func leaf).
	if err := os.MkdirAll(filepath.Join(outDir, appsDirName, "web", "functions", "index.func", "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}

	fns, err := collectFunctions(outDir)
	if err != nil {
		t.Fatalf("collectFunctions: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "web/api/todos/[id]", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/api/todos/[id].func", Framework: "next", App: "web"},
		{Name: "web/index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "apps/web/functions/index.func", Framework: "next", App: "web"},
	}
	if len(fns) != len(want) {
		t.Fatalf("collectFunctions returned %d, want %d: %+v", len(fns), len(want), fns)
	}
	for i, w := range want {
		if fns[i] != w {
			t.Errorf("function[%d] = %+v, want %+v", i, fns[i], w)
		}
	}
}

// Two apps exposing the same route path is what a flat output tree could not
// represent: one `.func` overwrote the other on disk, and even discovered
// separately they produced the same logical name and so the same Lambda.
func TestCollectFunctions_SameRouteInTwoApps_DoesNotCollide(t *testing.T) {
	outDir := t.TempDir()
	for _, app := range []string{"admin", "storefront"} {
		writeFuncConfig(t, outDir, app, filepath.Join("api", "documents.func"),
			functionConfig{Runtime: "nodejs24.x", Handler: "route.js", Framework: "next", ID: "/api/documents", App: app})
	}

	fns, err := collectFunctions(outDir)
	if err != nil {
		t.Fatalf("collectFunctions: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "admin/api/documents", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "apps/admin/functions/api/documents.func", Framework: "next", RouteID: "/api/documents", App: "admin"},
		{Name: "storefront/api/documents", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "apps/storefront/functions/api/documents.func", Framework: "next", RouteID: "/api/documents", App: "storefront"},
	}
	if len(fns) != len(want) {
		t.Fatalf("collectFunctions returned %d, want %d: %+v", len(fns), len(want), fns)
	}
	for i, w := range want {
		if fns[i] != w {
			t.Errorf("function[%d] = %+v, want %+v", i, fns[i], w)
		}
	}
}

func TestCollectFunctions_RouteIDFromConfig(t *testing.T) {
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
}

func TestCollectFunctions_NoFunctionsDir(t *testing.T) {
	fns, err := collectFunctions(t.TempDir())
	if err != nil {
		t.Fatalf("collectFunctions: %v", err)
	}
	if fns != nil {
		t.Errorf("collectFunctions = %+v, want nil", fns)
	}
}

func TestCollectFunctions_MissingConfig_Errors(t *testing.T) {
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, appsDirName, "web", "functions", "api.func"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := collectFunctions(outDir)
	if err == nil {
		t.Fatal("collectFunctions succeeded on a .func with no config.json, want error")
	}
	if !strings.Contains(err.Error(), "api.func") || !strings.Contains(err.Error(), configFileName) {
		t.Errorf("error = %q, want it to name the offending .func and config.json", err)
	}
}

func TestCollectFunctions_MissingField_Errors(t *testing.T) {
	outDir := t.TempDir()
	// framework omitted: all four fields are required.
	writeFuncConfig(t, outDir, "web", "api.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", App: "web"})

	_, err := collectFunctions(outDir)
	if err == nil {
		t.Fatal("collectFunctions succeeded on config missing framework, want error")
	}
	if !strings.Contains(err.Error(), "requires runtime, handler, framework, and app") {
		t.Errorf("error = %q, want it to explain the required fields", err)
	}
}

func TestCollectFunctions_InvalidJSON_Errors(t *testing.T) {
	outDir := t.TempDir()
	dir := filepath.Join(outDir, appsDirName, "web", "functions", "api.func")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := collectFunctions(outDir)
	if err == nil {
		t.Fatal("collectFunctions succeeded on invalid JSON, want error")
	}
	if !strings.Contains(err.Error(), "invalid "+configFileName) {
		t.Errorf("error = %q, want it to flag invalid config.json", err)
	}
}

// TestBuild_Integration materializes the embedded platform dist into the
// express-app fixture and spawns the real node builder over it with the user's
// node, then discovers the built function from its config.json. It is heavy
// (needs node + the fixture's installed node_modules) so it is skipped under
// -short.
func TestBuild_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: spawns real node over the builder")
	}

	fixtureRoot := repoRelPath(t, "cli", "platform", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, ".ocel")) })
	if err := platform.Ensure(fixtureRoot); err != nil {
		t.Fatalf("platform.Ensure: %v", err)
	}

	// The config Dir is the express-app itself so app Path "." points at the
	// fixture; node resolves express via the package's node_modules above it.
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
		Name:         "api/index",
		Runtime:      "nodejs24.x",
		Handler:      "src/server.js",
		ArtifactPath: "apps/api/functions/index.func",
		Framework:    "express",
		App:          "api",
	}
	if fns[0] != want {
		t.Errorf("function = %+v, want %+v", fns[0], want)
	}
}

// TestBuild_Integration_DetectsSingleApp proves the 0-apps path: with no apps
// configured, the real builder detects the express app at the project root and
// discovers its function. The function name is the sanitized root dir basename.
func TestBuild_Integration_DetectsSingleApp(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: spawns real node over the builder")
	}

	fixtureRoot := repoRelPath(t, "cli", "platform", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, ".ocel")) })
	if err := platform.Ensure(fixtureRoot); err != nil {
		t.Fatalf("platform.Ensure: %v", err)
	}

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
	if fns[0].Name != "express-app/index" || fns[0].Framework != "express" {
		t.Errorf("detected function = %+v, want name express-app/index framework express", fns[0])
	}
	// The detected app must still be named, so the manifest can carry it.
	if fns[0].App != "express-app" {
		t.Errorf("detected function app = %q, want %q", fns[0].App, "express-app")
	}
}

func repoRelPath(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// this file: <repo>/cli/internal/appbuilder/appbuilder_test.go
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	return filepath.Join(append([]string{repoRoot}, parts...)...)
}

func TestCollectFunctions_AppFromConfig(t *testing.T) {
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
}

func TestCollectFunctions_MissingApp_Errors(t *testing.T) {
	outDir := t.TempDir()
	writeFuncConfig(t, outDir, "web", "index.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express"})

	_, err := collectFunctions(outDir)
	if err == nil {
		t.Fatal("collectFunctions succeeded on config missing app, want error")
	}
	if !strings.Contains(err.Error(), "requires runtime, handler, framework, and app") {
		t.Errorf("error = %q, want it to explain the required fields", err)
	}
}

// TestBuild_ExportsResolvedValuesIntoTheBuildEnvironment proves a resolved
// value is present before the framework build runs, which is the only moment a
// framework can inline one into what it emits.
func TestBuild_ExportsResolvedValuesIntoTheBuildEnvironment(t *testing.T) {
	root := t.TempDir()
	writeBuilder(t, root)

	var got []string
	swapExec(t, func(_ context.Context, _ string, env []string, _ []byte, _ io.Writer) error {
		got = env
		return nil
	})

	vars := map[string]string{"POSTHOG_ID": "ph-123"}
	if err := Build(context.Background(), &projectconfig.Config{Dir: root}, vars, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if value, _ := lookup(got, "POSTHOG_ID"); value != "ph-123" {
		t.Fatalf("build environment POSTHOG_ID = %q, want the resolved value", value)
	}
}

// TestBuilderEnv_AddsResolvedValuesWithoutLosingTheBuildersOwn proves the
// export is additive: the builder's own entries and the inherited environment
// still reach it.
func TestBuilderEnv_AddsResolvedValuesWithoutLosingTheBuildersOwn(t *testing.T) {
	env := builderEnv("/adapters/next.js", "", map[string]string{"POSTHOG_ID": "ph-123"})

	if got, _ := lookup(env, "NEXT_ADAPTER_PATH"); got != "/adapters/next.js" {
		t.Errorf("NEXT_ADAPTER_PATH = %q, want the adapter path the builder needs", got)
	}
	if got, _ := lookup(env, "POSTHOG_ID"); got != "ph-123" {
		t.Errorf("POSTHOG_ID = %q, want the resolved value", got)
	}
	if len(env) <= 3 {
		t.Errorf("env holds %d entries, want the inherited environment as well", len(env))
	}
}

// TestBuild_TellsTheBuildWhichFolderTheAppBinds proves an app can read its own
// scoped key while it is being built. Resolution puts the value in the build
// environment under its bare name, but the SDK checks the binding before
// yielding it — so without this the app is told it is bound to the project root
// and a scoped read throws, with the value sitting right there.
func TestBuild_TellsTheBuildWhichFolderTheAppBinds(t *testing.T) {
	root := t.TempDir()
	writeBuilder(t, root)
	// A binding left over from some other process must not answer for this build.
	t.Setenv("OCEL_APP_FOLDER", "/stale")

	var got []string
	swapExec(t, func(_ context.Context, _ string, env []string, _ []byte, _ io.Writer) error {
		got = env
		return nil
	})

	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "apps/web", Framework: "next", Folder: "/web"}},
	}
	if err := Build(context.Background(), cfg, map[string]string{"API_URL": "https://web"}, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}

	value, found := lookup(got, "OCEL_APP_FOLDER")
	if !found {
		t.Fatal("the build was told no folder binding, so a scoped read during it cannot succeed")
	}
	if value != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the folder the app binds", value)
	}
}

// TestBuild_AppsBindingDifferentFoldersAreToldNoBinding proves the one thing a
// shared build process can honestly say when its apps disagree. Naming one
// app's folder would let the other read that app's scoped values; the project
// root makes an out-of-scope read the named error it already is.
func TestBuild_AppsBindingDifferentFoldersAreToldNoBinding(t *testing.T) {
	root := t.TempDir()
	writeBuilder(t, root)
	t.Setenv("OCEL_APP_FOLDER", "/stale")

	var got []string
	swapExec(t, func(_ context.Context, _ string, env []string, _ []byte, _ io.Writer) error {
		got = env
		return nil
	})

	cfg := &projectconfig.Config{
		Dir: root,
		Apps: []projectconfig.App{
			{Name: "web", Path: "apps/web", Framework: "next", Folder: "/web"},
			{Name: "admin", Path: "apps/admin", Framework: "next", Folder: "/admin"},
		},
	}
	if err := Build(context.Background(), cfg, nil, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}

	value, found := lookup(got, "OCEL_APP_FOLDER")
	if !found {
		t.Fatal("no binding was stated, so a stale one from the parent environment still answers")
	}
	if value != "" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the project root: one build cannot be bound to two folders", value)
	}
}

// TestBuild_RefusesAResolvedValueTheBuildEnvironmentOwns proves a declared
// variable cannot take a name the build process itself runs on. Applying it
// would break the build in a way that names nothing; ignoring it would drop a
// value the project declared. Refusing names the key while nothing has been
// built.
func TestBuild_RefusesAResolvedValueTheBuildEnvironmentOwns(t *testing.T) {
	for _, name := range []string{"PATH", "NEXT_ADAPTER_PATH", "OCEL_APP_FOLDER"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeBuilder(t, root)

			ran := false
			swapExec(t, func(_ context.Context, _ string, _ []string, _ []byte, _ io.Writer) error {
				ran = true
				return nil
			})

			err := Build(context.Background(), &projectconfig.Config{Dir: root}, map[string]string{name: "hijacked"}, io.Discard)
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
}

// TestBuilderEnv_WhatTheBuildOwnsIsAppliedLast proves the ordering the refusal
// above does not depend on: exec is last-wins, so the entries the build runs on
// are written after the resolved values, and no path into this function can
// repoint the builder.
func TestBuilderEnv_WhatTheBuildOwnsIsAppliedLast(t *testing.T) {
	env := builderEnv("/adapters/next.js", "/web", map[string]string{
		"NEXT_ADAPTER_PATH": "/evil/adapter.js",
		"OCEL_APP_FOLDER":   "/admin",
	})

	if got, _ := lookup(env, "NEXT_ADAPTER_PATH"); got != "/adapters/next.js" {
		t.Errorf("NEXT_ADAPTER_PATH = %q, want the builder's own adapter", got)
	}
	if got, _ := lookup(env, "OCEL_APP_FOLDER"); got != "/web" {
		t.Errorf("OCEL_APP_FOLDER = %q, want the binding the build was given", got)
	}
}
