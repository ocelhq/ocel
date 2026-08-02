package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// writePrebuiltFunction stages one `.func` in a project's build output exactly
// as the builder would, so a --prebuilt run has something real to discover.
func writePrebuiltFunction(t *testing.T, root, app, route string) {
	t.Helper()
	dir := filepath.Join(root, ".ocel", "output", "apps", app, "functions", route+".func")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]string{
		"runtime":   "nodejs24.x",
		"handler":   "index.handler",
		"framework": "express",
		"app":       app,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o644); err != nil {
		t.Fatal(err)
	}
}

// recordBuildApp replaces the app-build seam with one that records whether it ran.
func recordBuildApp(t *testing.T) *bool {
	t.Helper()
	ran := false
	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, map[string]string, io.Writer) error {
		ran = true
		return nil
	}
	t.Cleanup(func() { buildApp = prev })
	return &ran
}

func TestCollectAndBuildManifest_Prebuilt_SkipsTheBuild(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	ran := recordBuildApp(t)

	var out bytes.Buffer
	cfg := prebuiltConfig(root)
	manifest, err := collectAndBuildManifest(context.Background(), cfg, noGate(cfg), true, &out)
	if err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}
	if *ran {
		t.Error("the app build ran under --prebuilt, want it skipped")
	}

	functions := manifest.GetFunctions()
	if len(functions) != 1 {
		t.Fatalf("manifest carries %d functions, want the prebuilt one: %+v", len(functions), functions)
	}
	if got, want := functions[0].GetLogicalName(), "api_index"; got != want {
		t.Errorf("function logical name = %q, want %q", got, want)
	}
	if got, want := functions[0].GetArtifactPath(), "apps/api/functions/index.func"; got != want {
		t.Errorf("artifact path = %q, want the prebuilt tree's %q", got, want)
	}
	if !strings.Contains(out.String(), "prebuilt") {
		t.Errorf("build output = %q, want it to report that prebuilt output was used", out.String())
	}
}

func TestCollectAndBuildManifest_NotPrebuilt_RunsTheBuild(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	ran := recordBuildApp(t)

	cfg := prebuiltConfig(root)
	if _, err := collectAndBuildManifest(context.Background(), cfg, noGate(cfg), false, io.Discard); err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}
	if !*ran {
		t.Error("the app build was skipped without --prebuilt, want it to run")
	}
}

// A --prebuilt run against a project that was never built must say so rather
// than fall through to an infrastructure-only deploy.
func TestCollectAndBuildManifest_Prebuilt_NoOutput_Errors(t *testing.T) {
	recordBuildApp(t)

	cfg := prebuiltConfig(t.TempDir())
	_, err := collectAndBuildManifest(context.Background(), cfg, noGate(cfg), true, io.Discard)
	if err == nil {
		t.Fatal("collectAndBuildManifest succeeded with no build output, want error")
	}
	if !strings.Contains(err.Error(), "ocel build") {
		t.Errorf("error = %q, want it to point at `ocel build`", err)
	}
}

func TestPrebuiltFlag_RegisteredOnDeployAndPreview(t *testing.T) {
	for _, cmd := range []struct {
		name string
		want string
	}{
		{"deploy", "deploy"},
		{"preview", "preview"},
		{"preview up", "preview up"},
	} {
		t.Run(cmd.name, func(t *testing.T) {
			target := rootCmd
			for _, part := range strings.Fields(cmd.want) {
				next, _, err := target.Find([]string{part})
				if err != nil || next == target {
					t.Fatalf("command %q not found: %v", cmd.want, err)
				}
				target = next
			}
			if target.Flags().Lookup("prebuilt") == nil {
				t.Errorf("`ocel %s` does not accept --prebuilt", cmd.want)
			}
		})
	}
}

// prebuiltConfig is a resolved config with no discoverable resources, so the
// test turns entirely on the build half.
func prebuiltConfig(root string) *projectconfig.Config {
	return &projectconfig.Config{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []projectconfig.App{{Name: "api", Path: ".", Framework: "express", Compute: "serverless"}},
	}
}

// TestRunDeploy_Prebuilt_NoOutput_AbortsBeforeSpawn proves a --prebuilt deploy
// against an unbuilt checkout aborts before any provider is spawned.
func TestRunDeploy_Prebuilt_NoOutput_AbortsBeforeSpawn(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	recordBuildApp(t)
	prev := collectAppFunctions
	collectAppFunctions = appbuilder.CollectFunctions
	t.Cleanup(func() { collectAppFunctions = prev })

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want the missing build output reported")
	}
	if !strings.Contains(stdout.String(), "ocel build") {
		t.Errorf("stdout = %q, want it to point at `ocel build`", stdout.String())
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

// noGate is a gate over a store holding nothing, for the paths that declare
// no variables and so never consult it.
// noGate is a gate over a store that holds nothing. Its scope is the project's
// own, because that is what the deploy path builds it from: an app the scope
// does not name has no folder to resolve from.
func noGate(cfg *projectconfig.Config) *envgate.Gate {
	return envgate.New(emptyValues{}, envScope(cfg, false))
}

type emptyValues struct{}

func (emptyValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (emptyValues) Reveal(context.Context, []envgate.Cell) (map[envgate.Cell]string, error) {
	return nil, nil
}
