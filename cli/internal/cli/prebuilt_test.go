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
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

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

func recordBuildApp(t *testing.T) *bool {
	t.Helper()
	ran := false
	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
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

func TestCollectAndBuildManifest_GeneratesTheClientAccessorBeforeTheBuild(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	generated := ""
	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		data, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts"))
		if err != nil {
			return err
		}
		generated = string(data)
		return nil
	}
	t.Cleanup(func() { buildApp = prev })

	cfg := prebuiltConfig(root)
	if _, err := collectAndBuildManifest(context.Background(), cfg, clientValueGate(t, cfg, "https://example.com"), false, io.Discard); err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}

	if !strings.Contains(generated, `PUBLIC_SITE_URL: inlined("PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
		t.Errorf("accessor the build saw = %q, want it to read the key under its declared name", generated)
	}
	if _, err := os.Stat(filepath.Join(root, ".ocel", "output", "client-values.json")); err != nil {
		t.Errorf("the build recorded no client values: %v", err)
	}
}

func TestCollectAndBuildManifest_Prebuilt_RefusesAStaleClientValue(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	recordBuildApp(t)
	cfg := prebuiltConfig(root)

	if err := clientenv.Record(root, []clientenv.App{{Name: "api", Variables: []manifestbuilder.Variable{{
		Key:              "PUBLIC_SITE_URL",
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Value:            "https://example.com",
		ClientAccessible: true,
	}}}}); err != nil {
		t.Fatal(err)
	}

	gate := clientValueGate(t, cfg, "https://rotated.example.com")
	_, err := collectAndBuildManifest(context.Background(), cfg, gate, true, io.Discard)
	if err == nil {
		t.Fatal("collectAndBuildManifest = nil for a build predating the client value, want a refusal")
	}
	if !strings.Contains(err.Error(), "PUBLIC_SITE_URL") {
		t.Errorf("error = %q, want it to name the changed key", err)
	}
	if !strings.Contains(err.Error(), "--prebuilt") {
		t.Errorf("error = %q, want it to name the flag being refused", err)
	}
}

func TestCollectAndBuildManifest_Prebuilt_NamesAnOcelBuildOutputForWhatItIs(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	recordBuildApp(t)
	cfg := prebuiltConfig(root)
	if err := clientenv.RecordUnresolved(root); err != nil {
		t.Fatal(err)
	}

	_, err := collectAndBuildManifest(context.Background(), cfg, clientValueGate(t, cfg, "https://example.com"), true, io.Discard)
	if err == nil {
		t.Fatal("collectAndBuildManifest = nil for an `ocel build` output, want a refusal")
	}
	for _, want := range []string{"PUBLIC_SITE_URL", "never inlined", "`ocel build` resolves no values"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to state %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "changed since") {
		t.Errorf("error = %q, want it not to claim the value changed", err)
	}
}

func TestCollectAndBuildManifest_Prebuilt_ProceedsWhenTheClientValueIsUnchanged(t *testing.T) {
	root := t.TempDir()
	writePrebuiltFunction(t, root, "api", "index")
	recordBuildApp(t)
	cfg := prebuiltConfig(root)

	if err := clientenv.Record(root, []clientenv.App{{Name: "api", Variables: []manifestbuilder.Variable{{
		Key:              "PUBLIC_SITE_URL",
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Value:            "https://example.com",
		ClientAccessible: true,
	}}}}); err != nil {
		t.Fatal(err)
	}

	gate := clientValueGate(t, cfg, "https://example.com")
	if _, err := collectAndBuildManifest(context.Background(), cfg, gate, true, io.Discard); err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}
}

func clientValueGate(t *testing.T, cfg *projectconfig.Config, value string) *envgate.Gate {
	t.Helper()
	cell := envgate.Cell{Key: "PUBLIC_SITE_URL"}
	gate := envgate.New(oneValue{cell: cell, value: value}, envScope(cfg, false, ""))
	if err := gate.Prefetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := gate.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:              cell.Key,
			Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			ClientAccessible: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

type oneValue struct {
	cell  envgate.Cell
	value string
}

func (v oneValue) List(context.Context) ([]envgate.Stored, error) {
	return []envgate.Stored{{Address: envgate.Address{Cell: v.cell}}}, nil
}

func (v oneValue) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return map[envgate.Cell]string{v.cell: v.value}, nil
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

func prebuiltConfig(root string) *projectconfig.Config {
	return &projectconfig.Config{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []projectconfig.App{{Name: "api", Path: ".", Framework: "express", Compute: "serverless"}},
	}
}

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

func noGate(cfg *projectconfig.Config) *envgate.Gate {
	return envgate.New(emptyValues{}, envScope(cfg, false, ""))
}

type emptyValues struct{}

func (emptyValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (emptyValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return nil, nil
}
