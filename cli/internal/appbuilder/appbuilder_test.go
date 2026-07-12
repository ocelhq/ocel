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

func swapExec(t *testing.T, fn func(ctx context.Context, scriptPath string, request []byte, stderr io.Writer) ([]byte, error)) {
	t.Helper()
	prev := builderExec
	builderExec = fn
	t.Cleanup(func() { builderExec = prev })
}

func TestBuild_BuildsRequestAndParsesFunctions(t *testing.T) {
	root := t.TempDir()
	cfg := &projectconfig.Config{
		Dir: root,
		Apps: []projectconfig.App{
			{Name: "api", Path: "apps/api", Framework: "express", Entrypoint: "src/server.ts", Compute: "serverless"},
			{Name: "worker", Path: "apps/worker", Framework: "express", Compute: "serverless"},
		},
	}

	var gotScript string
	var gotReq builderRequest
	swapExec(t, func(_ context.Context, scriptPath string, request []byte, _ io.Writer) ([]byte, error) {
		gotScript = scriptPath
		if err := json.Unmarshal(request, &gotReq); err != nil {
			return nil, err
		}
		return []byte(`{"functions":[
			{"name":"api","logicalName":"Api","runtime":"nodejs24.x","handler":"index.handler","artifactPath":"functions/api.func","framework":"express"},
			{"name":"worker","runtime":"nodejs24.x","handler":"index.handler","artifactPath":"functions/worker.func","framework":"express"}
		]}` + "\n"), nil
	})

	fns, err := Build(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/api.func", Framework: "express"},
		{Name: "worker", Runtime: "nodejs24.x", Handler: "index.handler", ArtifactPath: "functions/worker.func", Framework: "express"},
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

	// The embedded builder bundle must be written under .ocel for node to run.
	if gotScript != filepath.Join(root, ".ocel", "node-builder.mjs") {
		t.Errorf("script path = %q, want .ocel/node-builder.mjs under root", gotScript)
	}
	written, err := os.ReadFile(gotScript)
	if err != nil {
		t.Fatalf("read written script: %v", err)
	}
	if !bytes.Equal(written, builderScript) {
		t.Errorf("written script differs from embedded bundle (%d vs %d bytes)", len(written), len(builderScript))
	}
}

func TestBuild_NoApps_ReturnsNil(t *testing.T) {
	swapExec(t, func(_ context.Context, _ string, _ []byte, _ io.Writer) ([]byte, error) {
		t.Fatal("builderExec should not run when there are no apps")
		return nil, nil
	})

	fns, err := Build(context.Background(), &projectconfig.Config{Dir: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if fns != nil {
		t.Errorf("Build returned %+v, want nil", fns)
	}
}

func TestBuild_BuildFailure_ReturnsClearError(t *testing.T) {
	cfg := &projectconfig.Config{
		Dir:  t.TempDir(),
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	swapExec(t, func(_ context.Context, _ string, _ []byte, _ io.Writer) ([]byte, error) {
		return nil, errors.New("node-builder failed: no entrypoint resolved for app \"api\"")
	})

	_, err := Build(context.Background(), cfg, io.Discard)
	if err == nil {
		t.Fatal("Build succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no entrypoint resolved") {
		t.Errorf("error = %q, want it to surface the node-builder failure", err)
	}
}

func TestBuild_UnparsableOutput_ReturnsError(t *testing.T) {
	cfg := &projectconfig.Config{
		Dir:  t.TempDir(),
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: "express", Compute: "serverless"}},
	}

	swapExec(t, func(_ context.Context, _ string, _ []byte, _ io.Writer) ([]byte, error) {
		return []byte("not json"), nil
	})

	if _, err := Build(context.Background(), cfg, io.Discard); err == nil {
		t.Fatal("Build succeeded on unparsable output, want error")
	}
}

// TestBuild_Integration spawns the real embedded bundle with the user's node
// over the express-app fixture. It is heavy (needs node + the fixture's
// installed node_modules) so it is skipped under -short.
func TestBuild_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: spawns real node over the embedded bundle")
	}

	fixtureRoot := repoRelPath(t, "packages", "node-builder", "test", "fixtures", "express-app")
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
	}
	if fns[0] != want {
		t.Errorf("function = %+v, want %+v", fns[0], want)
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
