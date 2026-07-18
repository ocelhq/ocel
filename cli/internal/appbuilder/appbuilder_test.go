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
)

func swapExec(t *testing.T, fn func(ctx context.Context, scriptPath string, request []byte, stderr io.Writer) error) {
	t.Helper()
	prev := builderExec
	builderExec = fn
	t.Cleanup(func() { builderExec = prev })
}

// writeFuncConfig simulates one thing the builder does: writing a `.func`
// directory with its config.json under outDir/functions.
func writeFuncConfig(t *testing.T, outDir, funcRel string, cfg functionConfig) {
	t.Helper()
	dir := filepath.Join(outDir, functionsDirName, funcRel)
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
	builderPath := filepath.Join(t.TempDir(), "cli.js")
	t.Setenv("OCEL_BUILDER_PATH", builderPath)
	cfg := &projectconfig.Config{
		Dir: root,
		Apps: []projectconfig.App{
			{Name: "api", Path: "apps/api", Framework: "express", Entrypoint: "src/server.ts", Compute: "serverless"},
			{Name: "worker", Path: "apps/worker", Framework: "express", Compute: "serverless"},
		},
	}

	var gotScript string
	var gotReq builderRequest
	swapExec(t, func(_ context.Context, scriptPath string, request []byte, _ io.Writer) error {
		gotScript = scriptPath
		if err := json.Unmarshal(request, &gotReq); err != nil {
			return err
		}
		// Simulate the builder writing its output tree.
		writeFuncConfig(t, gotReq.OutDir, "api.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "api"})
		writeFuncConfig(t, gotReq.OutDir, "worker.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express", App: "worker"})
		return nil
	})

	fns, err := Build(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/api.func", Framework: "express", App: "api"},
		{Name: "worker", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/worker.func", Framework: "express", App: "worker"},
	}
	if len(fns) != len(want) {
		t.Fatalf("Build returned %d functions, want %d: %+v", len(fns), len(want), fns)
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

	// The builder entry is read verbatim from OCEL_BUILDER_PATH.
	if gotScript != builderPath {
		t.Errorf("script path = %q, want %q", gotScript, builderPath)
	}
}

func TestBuild_MissingBuilderPath(t *testing.T) {
	t.Setenv("OCEL_BUILDER_PATH", "")
	cfg := &projectconfig.Config{
		Dir:  t.TempDir(),
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	_, err := Build(context.Background(), cfg, io.Discard)
	if err == nil {
		t.Fatal("Build succeeded with OCEL_BUILDER_PATH unset, want error")
	}
	if !strings.Contains(err.Error(), "OCEL_BUILDER_PATH") {
		t.Errorf("error = %q, want it to mention OCEL_BUILDER_PATH", err)
	}
}

func TestBuild_NoApps_RunsBuilderForDetectionAndResetsOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OCEL_BUILDER_PATH", filepath.Join(t.TempDir(), "cli.js"))
	// A stale artifact from a previous build must not survive to be deployed.
	writeFuncConfig(t, filepath.Join(root, ".ocel", "output"), "stale.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "h", Framework: "express", App: "stale"})

	var gotReq builderRequest
	swapExec(t, func(_ context.Context, _ string, request []byte, _ io.Writer) error {
		// Simulate the builder running detection and finding nothing to build.
		return json.Unmarshal(request, &gotReq)
	})

	fns, err := Build(context.Background(), &projectconfig.Config{Dir: root}, io.Discard)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if fns != nil {
		t.Errorf("Build returned %+v, want nil", fns)
	}
	if len(gotReq.Apps) != 0 {
		t.Errorf("request apps = %+v, want empty", gotReq.Apps)
	}
	if got, want := gotReq.ProjectRoot, root; got != want {
		t.Errorf("request projectRoot = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, ".ocel", "output", "functions", "stale.func")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale .func survived the reset (stat err = %v)", err)
	}
}

func TestBuild_BuildFailure_ReturnsClearError(t *testing.T) {
	t.Setenv("OCEL_BUILDER_PATH", filepath.Join(t.TempDir(), "cli.js"))
	cfg := &projectconfig.Config{
		Dir:  t.TempDir(),
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	swapExec(t, func(_ context.Context, _ string, _ []byte, _ io.Writer) error {
		return errors.New("node-builder failed: no entrypoint resolved for app \"api\"")
	})

	_, err := Build(context.Background(), cfg, io.Discard)
	if err == nil {
		t.Fatal("Build succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no entrypoint resolved") {
		t.Errorf("error = %q, want it to surface the node-builder failure", err)
	}
}

func TestCollectFunctions_Nested(t *testing.T) {
	outDir := t.TempDir()
	writeFuncConfig(t, outDir, filepath.Join("api", "todos", "[id].func"),
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
	writeFuncConfig(t, outDir, "index.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "next", App: "web"})
	// A nested node_modules with its own package.json must not be mistaken for
	// a function (no config.json, and it lives inside a .func leaf).
	if err := os.MkdirAll(filepath.Join(outDir, "functions", "index.func", "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}

	fns, err := collectFunctions(outDir)
	if err != nil {
		t.Fatalf("collectFunctions: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "api/todos/[id]", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/api/todos/[id].func", Framework: "next", App: "web"},
		{Name: "index", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/index.func", Framework: "next", App: "web"},
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
	writeFuncConfig(t, outDir, filepath.Join("api", "documents.func"),
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
	if err := os.MkdirAll(filepath.Join(outDir, "functions", "api.func"), 0o755); err != nil {
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
	writeFuncConfig(t, outDir, "api.func", functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", App: "web"})

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
	dir := filepath.Join(outDir, "functions", "api.func")
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

// TestBuild_Integration spawns the real node builder (resolved from
// OCEL_BUILDER_PATH) with the user's node over the express-app fixture, then
// discovers the built function from its config.json. It is heavy (needs node +
// the fixture's installed node_modules + the ocel package built) so it is
// skipped under -short.
func TestBuild_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: spawns real node over the builder")
	}

	ocelRoot := repoRelPath(t, "packages", "ocel")
	builderPath := filepath.Join(ocelRoot, "dist", "builder", "cli.js")
	if _, err := os.Stat(builderPath); err != nil {
		t.Skipf("builder not built: %v; run `pnpm --filter ocel build` first", err)
	}
	t.Setenv("OCEL_BUILDER_PATH", builderPath)

	fixtureRoot := repoRelPath(t, "packages", "ocel", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	// The config Dir is the express-app itself so app Path "." points at the
	// fixture; node resolves express via the package's node_modules above it.
	cfg := &projectconfig.Config{
		Dir:  fixtureRoot,
		Apps: []projectconfig.App{{Name: "api", Path: ".", Framework: "express", Compute: "serverless"}},
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, ".ocel")) })

	var stderr bytes.Buffer
	fns, err := Build(context.Background(), cfg, &stderr)
	if err != nil {
		t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
	}

	if len(fns) != 1 {
		t.Fatalf("Build returned %d functions, want 1: %+v", len(fns), fns)
	}
	want := manifestbuilder.Function{
		Name:         "api",
		Runtime:      "nodejs24.x",
		Handler:      "src/server.js",
		ArtifactPath: "functions/api.func",
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

	builderPath := filepath.Join(repoRelPath(t, "packages", "ocel"), "dist", "builder", "cli.js")
	if _, err := os.Stat(builderPath); err != nil {
		t.Skipf("builder not built: %v; run `pnpm --filter ocel build` first", err)
	}
	t.Setenv("OCEL_BUILDER_PATH", builderPath)

	fixtureRoot := repoRelPath(t, "packages", "ocel", "test", "fixtures", "express-app")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(fixtureRoot, ".ocel")) })

	var stderr bytes.Buffer
	fns, err := Build(context.Background(), &projectconfig.Config{Dir: fixtureRoot}, &stderr)
	if err != nil {
		t.Fatalf("Build: %v; stderr=%s", err, stderr.String())
	}

	if len(fns) != 1 {
		t.Fatalf("Build returned %d functions, want 1: %+v", len(fns), fns)
	}
	if fns[0].Name != "express-app" || fns[0].Framework != "express" {
		t.Errorf("detected function = %+v, want name express-app framework express", fns[0])
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
	writeFuncConfig(t, outDir, "api.func",
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
	writeFuncConfig(t, outDir, "api.func",
		functionConfig{Runtime: "nodejs24.x", Handler: "index.handler", Framework: "express"})

	_, err := collectFunctions(outDir)
	if err == nil {
		t.Fatal("collectFunctions succeeded on config missing app, want error")
	}
	if !strings.Contains(err.Error(), "requires runtime, handler, framework, and app") {
		t.Errorf("error = %q, want it to explain the required fields", err)
	}
}
