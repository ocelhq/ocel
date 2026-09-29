package deploy

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
	"github.com/spf13/cobra"
)

func manifestVariable(t *testing.T, manifest *contractv1.Manifest, app, key string) *contractv1.ManifestVariable {
	t.Helper()
	for _, a := range manifest.GetApps() {
		if a.GetName() != app {
			continue
		}
		for _, v := range a.GetVariables() {
			if v.GetKey() == key {
				return v
			}
		}
		keys := make([]string, 0, len(a.GetVariables()))
		for _, v := range a.GetVariables() {
			keys = append(keys, v.GetKey())
		}
		t.Fatalf("app %q has no %s among its variables %q", app, key, keys)
	}
	t.Fatalf("manifest has no app %q", app)
	return nil
}

func TestTheDeploymentURLReachesEveryDeliverySite(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	dependencies := newTestDependencies()
	stubRecordedDeploymentIDs(&dependencies)

	var built map[string]map[string]string
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, env map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		built = env
		return functionsOnDisk(cfg)
	}

	s, _ := newBuildSpan(t)
	cfg := prebuiltConfig(root)
	urls := map[string]string{"api": "https://api.acme.com"}
	manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s, urls: urls})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}

	t.Run("the build is handed it", func(t *testing.T) {
		if got, want := built["api"][processenv.AppURLEnvVar], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q", built["api"], processenv.AppURLEnvVar, want)
		}
		if got, want := built["api"][processenv.ClientURLEnvVar], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q for the browser bundle", built["api"], processenv.ClientURLEnvVar, want)
		}
	})

	t.Run("the manifest passes it to the provider", func(t *testing.T) {
		if got, want := manifestVariable(t, manifest, "api", processenv.AppURLEnvVar).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", processenv.AppURLEnvVar, got, want)
		}
		if got, want := manifestVariable(t, manifest, "api", processenv.ClientURLEnvVar).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", processenv.ClientURLEnvVar, got, want)
		}
	})

	t.Run("the client accessor inlines it", func(t *testing.T) {
		accessor, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if err != nil {
			t.Fatalf("no client accessor was generated: %v", err)
		}
		if !strings.Contains(string(accessor), processenv.ClientURLEnvVar) {
			t.Errorf("accessor = %s, want it to read %s", accessor, processenv.ClientURLEnvVar)
		}
	})
}

func TestPrebuiltRefusesAnOutputBuiltForAnotherURL(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	dependencies := newTestDependencies()
	recordBuildApp(&dependencies)
	cfg := prebuiltConfig(root)

	s, _ := newBuildSpan(t)
	if _, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s, urls: map[string]string{"api": "https://api.acme.com"}}); err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}

	s, _ = newBuildSpan(t)
	_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s, urls: map[string]string{"api": "https://pr-1.preview.acme.com"}})
	if err == nil {
		t.Fatal("collectBuildAndAssemble = nil for output built against another hostname, want a refusal: the url is inlined into the browser bundle, so this deploy would serve the wrong one")
	}
	if !strings.Contains(err.Error(), processenv.ClientURLEnvVar) {
		t.Errorf("error = %q, want it to name the key whose value changed", err)
	}
}

func TestTheManifestNamesEveryAppsCompute(t *testing.T) {
	t.Run("an app the config names has the compute resolved onto it", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)

		s, _ := newBuildSpan(t)
		cfg := &project.Project{
			Dir:  root,
			Slug: "prebuilt",
			Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
		}
		stubPrebuiltFromDisk(&dependencies)
		stubAppImages(&dependencies, "api")
		manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if got := computeOf(t, manifest, "api"); got != "container" {
			t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
		}
	})

	t.Run("an app only the build names cannot land on the provider's container default", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)

		s, _ := newBuildSpan(t)
		cfg := &project.Project{Dir: root, Slug: "prebuilt"}
		_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble() landed an app the config never names on container compute, so a provider would be handed an app with no image")
		}
		if !strings.Contains(err.Error(), `"api"`) {
			t.Errorf("collectBuildAndAssemble() error = %q, want it to name the app", err)
		}
	})
}

func onCompute(cfg *project.Project, compute provider.Compute) *project.Project {
	resolved := *cfg
	resolved.Apps = make([]project.App, len(cfg.Apps))
	for i, app := range cfg.Apps {
		if app.Compute == "" {
			app.Compute = compute
		}
		resolved.Apps[i] = app
	}
	return &resolved
}

func computeOf(t *testing.T, manifest *contractv1.Manifest, app string) string {
	t.Helper()
	for _, candidate := range manifest.GetApps() {
		if candidate.GetName() == app {
			return string(provider.ComputeOf(candidate))
		}
	}
	names := make([]string, 0, len(manifest.GetApps()))
	for _, candidate := range manifest.GetApps() {
		names = append(names, candidate.GetName())
	}
	t.Fatalf("manifest has no app %q among its apps %q", app, names)
	return ""
}

func TestAContainerAppThatNamesNoRuntimeStillReachesTheProvider(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	dependencies := newTestDependencies()
	recordBuildApp(&dependencies)

	s, _ := newBuildSpan(t)
	cfg := &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
	}
	stubPrebuiltFromDisk(&dependencies)
	stubAppImages(&dependencies, "api")

	manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble over a container app with no runtime: %v", err)
	}
	if got := computeOf(t, manifest, "api"); got != "container" {
		t.Errorf("manifest app %q compute = %q, want %q", "api", got, "container")
	}
}

func definition(key string, class resourcesv1.VariableClass) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: class}
}

func TestAnAppsVariablesPairEachDeclarationWithItsResolvedValue(t *testing.T) {
	t.Parallel()

	t.Run("pairs each declaration with what was resolved for it", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("POSTHOG_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("WEBHOOK_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"POSTHOG_ID":     {Value: "ph-123"},
			"WEBHOOK_SECRET": {},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "POSTHOG_ID" || got[0].Value != "ph-123" ||
			got[0].Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			t.Errorf("POSTHOG_ID = %+v, want its class and its resolved value", got[0])
		}
		if got[1].Value != "" {
			t.Errorf("WEBHOOK_SECRET = %+v, want no value: a live value never reaches a build host", got[1])
		}
	})

	t.Run("omits a key this app cannot read", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("CHECKOUT_ONLY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		}

		if got := appVariables(definitions, map[string]variables.ResolvedValue{}); len(got) != 0 {
			t.Fatalf("appVariables = %+v, want nothing for a key this app resolves no cell for", got)
		}
	})

	t.Run("keeps client accessibility from the declaration", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			{Key: "PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, ClientAccessible: true},
			{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		}
		resolved := map[string]variables.ResolvedValue{
			"PUBLIC_SITE_URL": {Value: "https://example.com"},
			"INTERNAL_URL":    {Value: "http://internal"},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if !got[0].ClientAccessible {
			t.Errorf("PUBLIC_SITE_URL = %+v, want it marked client-accessible", got[0])
		}
		if got[1].ClientAccessible {
			t.Errorf("INTERNAL_URL = %+v, want it left server-only", got[1])
		}
	})

	t.Run("keeps the version each value resolved at", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("PLAIN_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("LIVE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"PLAIN_KEY": {Value: "v", Version: 2},
			"LIVE_KEY":  {Version: 9},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "PLAIN_KEY" || got[0].Version != 2 {
			t.Errorf("PLAIN_KEY = %+v, want the version its cell resolved at", got[0])
		}
		if got[1].Key != "LIVE_KEY" || got[1].Version != 9 {
			t.Errorf("LIVE_KEY = %+v, want its cell's version included too", got[1])
		}
	})

	t.Run("keeps the folder each key resolved from", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("ROOT_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			definition("SCOPED_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"ROOT_KEY":   {},
			"SCOPED_KEY": {Folder: "/admin"},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "ROOT_KEY" || got[0].Folder != "" {
			t.Errorf("ROOT_KEY = %+v, want the empty root spelling, never the store's %q sentinel", got[0], "/")
		}
		if got[1].Key != "SCOPED_KEY" || got[1].Folder != "/admin" {
			t.Errorf("SCOPED_KEY = %+v, want the folder it resolved from", got[1])
		}
	})
}

func TestACapturedBuildOutputIsCappedAndAppendedToTheError(t *testing.T) {
	t.Run("passes the error through untouched when nothing was captured", func(t *testing.T) {
		c := &boundedCapture{}
		err := errors.New("exit status 1")
		got := c.annotate(err)
		if !errors.Is(got, err) {
			t.Errorf("annotate() = %v, want the original error unwrapped", got)
		}
	})

	t.Run("appends captured output to the error", func(t *testing.T) {
		c := &boundedCapture{}
		if _, err := c.Write([]byte("Error: read STRIPE_API_KEY (project root): unavailable\n")); err != nil {
			t.Fatalf("Write() = %v", err)
		}
		err := c.annotate(errors.New("exit status 1"))
		if !strings.Contains(err.Error(), "STRIPE_API_KEY (project root)") {
			t.Errorf("annotate() = %q, want it to include the captured detail", err)
		}
		if !strings.Contains(err.Error(), "exit status 1") {
			t.Errorf("annotate() = %q, want the original error preserved", err)
		}
	})

	t.Run("caps how much it captures", func(t *testing.T) {
		c := &boundedCapture{}
		chunk := strings.Repeat("x", 1024)
		for i := 0; i < 8; i++ {
			if _, err := c.Write([]byte(chunk)); err != nil {
				t.Fatalf("Write() = %v", err)
			}
		}
		if got := c.buf.Len(); got != maxCapturedDiscoveryOutput {
			t.Errorf("captured %d bytes, want it capped at %d", got, maxCapturedDiscoveryOutput)
		}
	})

	t.Run("Write always reports the full length written, even once capped", func(t *testing.T) {
		c := &boundedCapture{}
		p := []byte(strings.Repeat("y", maxCapturedDiscoveryOutput+100))
		n, err := c.Write(p)
		if err != nil {
			t.Fatalf("Write() = %v", err)
		}
		if n != len(p) {
			t.Errorf("Write() n = %d, want %d so io.MultiWriter doesn't treat this as a short write", n, len(p))
		}
	})
}

func newBuildSpan(t *testing.T) (*run.Span, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewSink(terminal.Resolve(terminal.Conditions{Verbose: true}), &out))
	t.Cleanup(func() { _ = bus.Close() })
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}
	return run.Phase(progressv1.Phase_PHASE_BUILD), &out
}

func recordBuildApp(dependencies *Dependencies) *bool {
	stubRecordedDeploymentIDs(dependencies)
	ran := false
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		ran = true
		return functionsOnDisk(cfg)
	}
	return &ran
}

func functionsOnDisk(cfg *project.Project) (build.Output, error) {
	functions, err := build.ReadFunctions(cfg.Dir)
	if errors.Is(err, build.ErrNoBuildOutput) {
		return build.Output{}, nil
	}
	return build.Output{Functions: functions}, err
}

func declarationsWithClientValue(t *testing.T, cfg *project.Project, value string) *variables.Declarations {
	t.Helper()
	cell := variables.Cell{Key: "PUBLIC_SITE_URL"}
	declarations := variables.NewDeclarations(oneValue{cell: cell, value: value}, variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, ""))
	if err := declarations.Prefetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := declarations.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:              cell.Key,
			Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			ClientAccessible: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return declarations
}

type oneValue struct {
	cell  variables.Cell
	value string
}

func (v oneValue) List(context.Context) ([]variables.ValueMetadata, error) {
	return []variables.ValueMetadata{{Coordinate: variables.Coordinate{Cell: v.cell}}}, nil
}

func (v oneValue) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return map[variables.Coordinate]string{{Cell: v.cell}: v.value}, nil
}

func prebuiltConfig(root string) *project.Project {
	return &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "serverless", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNode}}},
	}
}

func emptyDeclarations(cfg *project.Project) *variables.Declarations {
	return variables.NewDeclarations(emptyValues{}, variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, ""))
}

type emptyValues struct{}

func (emptyValues) List(context.Context) ([]variables.ValueMetadata, error) { return nil, nil }

func (emptyValues) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return nil, nil
}

func recordedClientValue() clientenv.App {
	return clientenv.App{Name: "api", Variables: []variables.Variable{{
		Key:              "PUBLIC_SITE_URL",
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Value:            "https://example.com",
		ClientAccessible: true,
	}}}
}

func TestPrebuiltSkipsTheBuildAndDeploysTheRecordedOutput(t *testing.T) {
	t.Run("--prebuilt skips the build and deploys the prebuilt tree's function", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		ran := recordBuildApp(&dependencies)

		s, out := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if *ran {
			t.Error("the app build ran under --prebuilt, want it skipped")
		}

		functions := manifest.GetApps()[0].GetServerless().GetFunctions()
		if len(functions) != 1 {
			t.Fatalf("manifest has %d functions, want the prebuilt one: %+v", len(functions), functions)
		}
		if got, want := functions[0].GetLogicalName(), "fn--api--index"; got != want {
			t.Errorf("function logical name = %q, want %q", got, want)
		}
		if got, want := functions[0].GetArtifactPath(), "apps/api/functions/index.func"; got != want {
			t.Errorf("artifact path = %q, want the prebuilt tree's %q", got, want)
		}
		if !strings.Contains(out.String(), "prebuilt") {
			t.Errorf("build output = %q, want it to report that prebuilt output was used", out.String())
		}
	})

	t.Run("without --prebuilt the build runs", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		ran := recordBuildApp(&dependencies)

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		if _, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if !*ran {
			t.Error("the app build was skipped without --prebuilt, want it to run")
		}
	})

	t.Run("--prebuilt with no build output errors", func(t *testing.T) {
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(t.TempDir())
		_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble succeeded with no build output, want error")
		}
		if !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("error = %q, want it to point at `ocel build`", err)
		}
	})

	t.Run("--prebuilt passes the id the output tree recorded for each app", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		recorded := "d1a2b3c4d5e6f708192a3b4c5d6e7f80"
		clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", "api", "deployment-id"), recorded+"\n")
		dependencies := newTestDependencies()

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		apps := manifest.GetApps()
		if len(apps) != 1 || apps[0].GetDeploymentId() != recorded {
			ids := make([]string, 0, len(apps))
			for _, app := range apps {
				ids = append(ids, app.GetDeploymentId())
			}
			t.Errorf("manifest apps have deployments %q, want api alone having %q", ids, recorded)
		}
	})

	t.Run("--prebuilt refuses an app the output tree recorded no id for", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble succeeded for an app no build stamped, want error")
		}
		if !strings.Contains(err.Error(), "ocel build") || !strings.Contains(err.Error(), "api") {
			t.Errorf("error = %q, want it to name the app and point at `ocel build`", err)
		}
	})

	t.Run("the client accessor is generated before the build", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		generated := ""
		dependencies := newTestDependencies()
		stubRecordedDeploymentIDs(&dependencies)
		dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
			data, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
			if err != nil {
				return build.Output{}, err
			}
			generated = string(data)
			return functionsOnDisk(cfg)
		}

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		if _, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarationsWithClientValue(t, cfg, "https://example.com"), phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}

		if !strings.Contains(generated, `PUBLIC_SITE_URL: inlined(schema, "PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
			t.Errorf("accessor the build saw = %q, want it to read the key under its declared name", generated)
		}
		if _, err := os.Stat(filepath.Join(root, statedir.Name, "output", "client-digests.json")); err != nil {
			t.Errorf("the build recorded no client values: %v", err)
		}
	})

	t.Run("--prebuilt refuses a stale client value", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)
		cfg := prebuiltConfig(root)

		if err := clientenv.Record(root, []clientenv.App{recordedClientValue()}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		declarations := declarationsWithClientValue(t, cfg, "https://rotated.example.com")
		_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarations, prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble = nil for a build predating the client value, want a refusal")
		}
		if !strings.Contains(err.Error(), "PUBLIC_SITE_URL") {
			t.Errorf("error = %q, want it to name the changed key", err)
		}
		if !strings.Contains(err.Error(), "--prebuilt") {
			t.Errorf("error = %q, want it to name the flag being refused", err)
		}
	})

	t.Run("--prebuilt names an `ocel build` output for what it is", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)
		cfg := prebuiltConfig(root)
		if err := clientenv.Record(root, []clientenv.App{{Name: "api"}}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarationsWithClientValue(t, cfg, "https://example.com"), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble = nil for an `ocel build` output, want a refusal")
		}
		for _, want := range []string{"PUBLIC_SITE_URL", "never inlined", "`ocel build`, which resolves no values"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "changed since") {
			t.Errorf("error = %q, want it not to claim the value changed", err)
		}
	})

	t.Run("--prebuilt proceeds when the client value is unchanged", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)
		cfg := prebuiltConfig(root)

		if err := clientenv.Record(root, []clientenv.App{recordedClientValue()}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		declarations := declarationsWithClientValue(t, cfg, "https://example.com")
		if _, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarations, prebuilt: true, phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
	})
}

func TestEveryDeployingCommandTakesPrebuilt(t *testing.T) {
	for _, cmd := range []struct {
		name string
		want string
	}{
		{"deploy", "deploy"},
		{"preview", "preview"},
		{"preview up", "preview up"},
	} {
		t.Run("`ocel "+cmd.name+"` accepts --prebuilt", func(t *testing.T) {
			parts := strings.Fields(cmd.want)
			commands := map[string]*cobra.Command{
				"deploy":  NewCommand(Dependencies{}),
				"preview": NewPreviewCommand(Dependencies{}),
			}
			target := commands[parts[0]]
			for _, part := range parts[1:] {
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

func TestAPrebuiltDeployWithNoBuildOutputStopsBeforeTheProvider(t *testing.T) {
	t.Run("no build output aborts before the provider is spawned", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		if err := os.RemoveAll(filepath.Join(root, statedir.Name, "output")); err != nil {
			t.Fatalf("drop the fixture's build output: %v", err)
		}
		dependencies := newTestDependencies()
		recordBuildApp(&dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the missing build output reported")
		}
		if !strings.Contains(stdout.String(), "ocel build") {
			t.Errorf("stdout = %q, want it to point at `ocel build`", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})
}

func TestPrebuiltDeploysTheImageTheBuildRecordedRatherThanBuildingOne(t *testing.T) {
	root := t.TempDir()
	dependencies := newTestDependencies()
	ran := recordBuildApp(&dependencies)
	var asked map[string]string
	dependencies.ReadPrebuilt = func(_ context.Context, _ *project.Project, archs map[string]string) (build.Output, error) {
		asked = archs
		return build.Output{Images: map[string]string{"api": clitest.FixtureImage("api")}}, nil
	}

	s, _ := newBuildSpan(t)
	cfg := &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
	}
	archs := map[string]string{"api": "arm64"}
	manifest, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s, containerArchs: archs})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}
	if *ran {
		t.Error("--prebuilt built the apps again, want the image the build recorded deployed as it is")
	}
	if !maps.Equal(asked, archs) {
		t.Errorf("the prebuilt image was checked against %v, want the %v the provider runs", asked, archs)
	}
	apps := manifest.GetApps()
	if len(apps) != 1 || apps[0].GetName() != "api" || apps[0].GetContainer().GetImage() != clitest.FixtureImage("api") {
		var deployed []string
		for _, a := range apps {
			deployed = append(deployed, a.GetName()+" on "+a.GetContainer().GetImage())
		}
		t.Errorf("the manifest deploys %v, want api on the prebuilt %q", deployed, clitest.FixtureImage("api"))
	}
}

func stubPrebuiltFromDisk(dependencies *Dependencies) {
	dependencies.ReadPrebuilt = func(_ context.Context, cfg *project.Project, _ map[string]string) (build.Output, error) {
		functions, err := build.ReadFunctions(cfg.Dir)
		return build.Output{Functions: functions}, err
	}
}
