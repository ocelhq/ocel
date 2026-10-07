package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/statedir"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

func TestTheBuildIsHandedTheWorkersEachAppHosts(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	dependencies := newTestDependencies()
	stubRecordedDeploymentIDs(&dependencies)
	source := filepath.Join(root, discovery.DefaultRootDirName, "jobs.ts") + ":4"
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return []declaration.Resource{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "greet", Task: &resourcesv1.TaskConfig{}, Source: source}}, nil
	}
	var handed build.HostedWorkers
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, workers build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		handed = workers
		return functionsOnDisk(cfg)
	}

	s, _ := newBuildSpan(t)
	cfg := prebuiltConfig(root)
	if _, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s}); err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}
	if len(handed) != 1 || len(handed["api"]) != 1 || handed["api"][0] != source {
		t.Errorf("the build was handed workers %v, want api hosting the default worker that serves greet", handed)
	}
}

func TestTheDeploymentURLReachesEveryDeliverySite(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	dependencies := newTestDependencies()
	stubRecordedDeploymentIDs(&dependencies)

	var built map[string]build.AppVariables
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, variables map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = variables
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
		if got, want := built["api"].Env[processenv.AppURLEnvVar], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q", built["api"].Env, processenv.AppURLEnvVar, want)
		}
		if got, want := built["api"].Env[processenv.ClientURLEnvVar], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q for the browser bundle", built["api"].Env, processenv.ClientURLEnvVar, want)
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

func TestADeployWarnsAboutAStandaloneNextContainerBeforeItBuilds(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web", "next.config.mjs"), []byte(`export default { output: "standalone" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	dependencies := newTestDependencies()
	recordBuildApp(&dependencies)
	s, out := newBuildSpan(t)
	warned := false
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		warned = strings.Contains(out.String(), `app "web" sets output: "standalone" in next.config.mjs`)
		return buildApps(ctx, cfg, variables, archs, workers, host, log)
	}
	cfg := &project.Project{
		Dir:  root,
		Slug: "standalone",
		Apps: []project.App{{Name: "web", Path: "web", Compute: provider.ComputeContainer, Container: &project.Container{Framework: buildoutput.FrameworkNext}}},
	}
	stubAppImages(&dependencies, "web")

	_, _, err := collectBuildAndAssemble(context.Background(), dependencies, assembly{cfg: cfg, declarations: emptyDeclarations(cfg), phase: s, span: s, host: build.Host{ShipsNextServerRuntime: true}})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble() error = %v", err)
	}
	if !warned {
		t.Errorf("the build began before the standalone warning, output so far:\n%s", out)
	}
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
			definition("PAGE_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("WEBHOOK_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"PAGE_ID":        {Value: "page-123"},
			"WEBHOOK_SECRET": {},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "PAGE_ID" || got[0].Value != "page-123" ||
			got[0].Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			t.Errorf("PAGE_ID = %+v, want its class and its resolved value", got[0])
		}
		if got[1].Value != "" {
			t.Errorf("WEBHOOK_SECRET = %+v, want no value: a secret's plaintext never enters the manifest", got[1])
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
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
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
		dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
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
	fixture := setUpDeployProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	if err := os.RemoveAll(filepath.Join(fixture.Root, statedir.Name, "output")); err != nil {
		t.Fatalf("drop the fixture's build output: %v", err)
	}
	dependencies := newTestDependencies()
	recordBuildApp(&dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want the missing build output reported")
	}
	if !strings.Contains(stdout.String(), "ocel build") {
		t.Errorf("stdout = %q, want it to point at `ocel build`", stdout.String())
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys, want none without a build to deploy", len(sent))
	}
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

func deployWith(t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, opts deployOptions) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func TestAnAppBuildsItsFunctionsIntoTheManifest(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpDeployProject(t)
	addAppToFixtureConfig(t, fixture.Root)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	functions := manifestApp(t, sentDeploy(t, fixture).GetManifest(), "api").GetServerless().GetFunctions()
	if len(functions) != 1 {
		t.Fatalf("api has %d functions in the manifest, want the one it built", len(functions))
	}
	fn := functions[0]
	if fn.GetLogicalName() != "fn--api--api" || fn.GetFramework().GetName() != "node" || fn.GetEntryFile() != "src/server.js" || fn.GetArtifactPath() != "output/api" {
		t.Errorf("function = %v, want fn--api--api built by node from src/server.js at output/api", fn)
	}
	if strings.Contains(out, "deploys only the") {
		t.Errorf("output = %q, want no infra-only note when a function is built", out)
	}
	if !strings.Contains(out, "Deployed") {
		t.Errorf("output = %q, want a terminal success message", out)
	}
}

func TestAProjectWhoseAppsBuildNothingDeploysItsResourcesAlone(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	if !strings.Contains(out, "No app has a function or image to deploy, so this deploys only the 1 resource test-app declares") {
		t.Errorf("output = %q, want the infra-only note naming how many resources deploy", out)
	}
	if !strings.Contains(out, "INFO  [build] ✓ test-app: Collected the resources test-app declares in ") {
		t.Errorf("output = %q, want the build span to say it only collects what the project declares", out)
	}
	if !strings.Contains(out, "Deployed") {
		t.Errorf("output = %q, want resources to still deploy to success", out)
	}
	manifest := sentDeploy(t, fixture).GetManifest()
	if len(manifest.GetApps()) != 0 {
		t.Errorf("the manifest carries %d apps, want none when nothing was built", len(manifest.GetApps()))
	}
	if len(manifest.GetResources()) != 1 || manifest.GetResources()[0].GetLogicalName() != "db--main" {
		t.Errorf("the manifest carries resources %v, want db--main alone", manifest.GetResources())
	}
}

func TestAnAppBuildFailureStopsTheDeployBeforeTheProviderDeploysAnything(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("boom: app build failed")
	}
	fixture := setUpDeployProject(t)
	addAppToFixtureConfig(t, fixture.Root)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy err = nil, want the app-build failure")
	}
	if !strings.Contains(out, "boom: app build failed") {
		t.Errorf("output = %q, want the app-build failure surfaced", out)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys, want none after a failed build", len(sent))
	}
}

func TestADeployAttributesEachFunctionToTheAppThatBuiltIt(t *testing.T) {
	t.Run("a single app produces exactly one attributed app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpDeployProject(t)
		writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api", framework: "node", domains: { production: "Api.Acme.com" } }`)
		writeAppSource(t, fixture.Root, "api")

		if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		manifest := sentDeploy(t, fixture).GetManifest()
		if len(manifest.GetApps()) != 1 {
			t.Fatalf("the manifest carries %d apps, want exactly 1", len(manifest.GetApps()))
		}
		api := manifestApp(t, manifest, "api")
		if api.GetFramework().GetName() != "node" || !slices.Equal(productionHostnames(api), []string{"api.acme.com"}) {
			t.Errorf("api = %s on %v, want node with its own production domain, lower-cased", api.GetFramework().GetName(), productionHostnames(api))
		}
		if api.GetDeploymentId() != recordedDeploymentID("api") {
			t.Errorf("api deployment = %q, want the id its build recorded", api.GetDeploymentId())
		}
		if functions := api.GetServerless().GetFunctions(); len(functions) != 1 || functions[0].GetArtifactPath() != "output/api" {
			t.Errorf("api functions = %v, want the api function attributed to it", functions)
		}
	})

	t.Run("two apps attribute their functions to their own app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "web", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
			{Route: "admin", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/admin", App: "admin"},
		})
		fixture := setUpDeployProject(t)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  apps: [
    { name: "web", path: "apps/web", framework: "node", domains: { production: "acme.com" } },
    { name: "admin", path: "apps/admin", framework: "node" },
  ],
};
`)
		writeAppSource(t, fixture.Root, "web", "admin")

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, "INFO  [build] ✓ test-app: Collected the resources test-app declares in ") {
			t.Errorf("output = %q, want the build phase to say whose resources it collected", out)
		}
		manifest := sentDeploy(t, fixture).GetManifest()
		if len(manifest.GetApps()) != 2 {
			t.Fatalf("the manifest carries %d apps, want exactly 2", len(manifest.GetApps()))
		}
		if hostnames := productionHostnames(manifestApp(t, manifest, "admin")); len(hostnames) != 0 {
			t.Errorf("admin serves %v, want no domain of its own", hostnames)
		}
		if hostnames := productionHostnames(manifestApp(t, manifest, "web")); !slices.Equal(hostnames, []string{"acme.com"}) {
			t.Errorf("web serves %v, want its own production domain", hostnames)
		}
		for _, app := range []string{"web", "admin"} {
			a := manifestApp(t, manifest, app)
			functions := a.GetServerless().GetFunctions()
			if len(functions) != 1 || functions[0].GetLogicalName() != "fn--"+app+"--"+app || functions[0].GetArtifactPath() != "output/"+app {
				t.Errorf("%s functions = %v, want its own function attributed to it", app, functions)
			}
			if a.GetDeploymentId() != recordedDeploymentID(app) {
				t.Errorf("%s deployment = %q, want the id its own build recorded", app, a.GetDeploymentId())
			}
		}
		if recordedDeploymentID("web") == recordedDeploymentID("admin") {
			t.Fatal("the fixture gives both apps one id, so this proves nothing")
		}
	})

	t.Run("the app at the root of a project naming none appears in the manifest under its slug", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "h.js", ArtifactPath: "output/index", App: clitest.FixtureSlug},
		})
		fixture := setUpDeployProject(t)
		writeRootApp(t, fixture.Root)

		if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		manifest := sentDeploy(t, fixture).GetManifest()
		if len(manifest.GetApps()) != 1 {
			t.Fatalf("the manifest carries %d apps, want exactly 1", len(manifest.GetApps()))
		}
		if root := manifestApp(t, manifest, clitest.FixtureSlug); root.GetFramework().GetName() != "node" {
			t.Errorf("the root app is framework %q, want node", root.GetFramework().GetName())
		}
	})
}

func twoAppProject(t *testing.T) (Dependencies, clitest.FakeProject) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	writeAppsConfig(t, fixture.Root, `
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  `)
	writeAppSource(t, fixture.Root, "web", "api")
	return dependencies, fixture
}

func buildingEach(failing string) func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
	return func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "the builder started\n")
		for _, app := range cfg.Apps {
			log, ended := out.App(app.Name)
			_, _ = fmt.Fprintf(log, "compiling %s\n", app.Name)
			if app.Name == failing {
				err := errors.New(app.Name + " did not compile")
				ended(err)
				return build.Output{}, err
			}
			ended(nil)
		}
		return build.Output{}, nil
	}
}

type buildScope struct {
	subject, message string
	started, ended   int
	status           progressv1.SpanStatus
	output           []string
}

func buildScopes(t *testing.T, stream string) ([]*buildScope, []string) {
	t.Helper()
	var scopes []*buildScope
	bySpan := map[string]*buildScope{}
	var phaseOutput []string
	for i, ev := range envelopes(t, stream) {
		if ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD {
			continue
		}
		span := string(ev.GetOperation().GetSpanId())
		switch body := ev.GetOperation().GetBody().(type) {
		case *progressv1.OperationEvent_Started:
			if ev.GetOperation().GetSubject() == "" || ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_DEBUG {
				continue
			}
			scope := &buildScope{subject: ev.GetOperation().GetSubject(), message: ev.GetOperation().GetMessage(), started: i}
			bySpan[span] = scope
			scopes = append(scopes, scope)
		case *progressv1.OperationEvent_Ended:
			if scope, ok := bySpan[span]; ok {
				scope.ended, scope.status = i, body.Ended.GetStatus()
			}
		case *progressv1.OperationEvent_Output:
			if scope, ok := bySpan[span]; ok {
				scope.output = append(scope.output, ev.GetOperation().GetMessage())
			} else {
				phaseOutput = append(phaseOutput, ev.GetOperation().GetMessage())
			}
		}
	}
	return scopes, phaseOutput
}

func TestEachAppBuildsAsASpanOfItsOwnInTheBuildPhaseOnceTheDeclarationsAreCollected(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.BuildApps = buildingEach("")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}

	scopes, phaseOutput := buildScopes(t, out)
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+": "+scope.message)
	}
	want := []string{
		clitest.FixtureSlug + ": Collecting the resources " + clitest.FixtureSlug + " declares",
		"web: Building app web",
		"api: Building app api",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build spans =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, scope := range scopes {
		if scope.status != progressv1.SpanStatus_SPAN_STATUS_OK {
			t.Errorf("%s ended %s, want OK", scope.subject, scope.status)
		}
		if i > 0 && scopes[i-1].ended > scope.started {
			t.Errorf("%s started before %s ended, want each span to end before the next begins", scope.subject, scopes[i-1].subject)
		}
	}
	for _, scope := range scopes[1:] {
		if want := "compiling " + scope.subject; strings.Join(scope.output, "\n") != want {
			t.Errorf("%s's output = %q, want %q", scope.subject, scope.output, want)
		}
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what no app's build said and nothing else", phaseOutput)
	}
}

func TestAnAppWhoseBuildFailsEndsItsOwnSpanInFailureAndTheDeployWithIt(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.BuildApps = buildingEach("api")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy succeeded, want api's build failure")
	}

	scopes, _ := buildScopes(t, out)
	statuses := map[string]progressv1.SpanStatus{}
	for _, scope := range scopes {
		statuses[scope.subject] = scope.status
	}
	if statuses["web"] != progressv1.SpanStatus_SPAN_STATUS_OK || statuses["api"] != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("span statuses = %v, want web OK and api ERROR", statuses)
	}
	if statuses[clitest.FixtureSlug] != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the collecting span ended %s, want OK: it finished before any app built", statuses[clitest.FixtureSlug])
	}
}

func TestEachAppsBuildPrintsAsABlockOfItsOwnWhenThatAppFinishes(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.Presentation = func(io.Writer) terminal.Presentation { return terminal.Resolve(terminal.Conditions{}) }
	dependencies.BuildApps = buildingEach("")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	for _, app := range []string{"web", "api"} {
		block := "INFO  [build] ✓ " + app + ": Built app " + app + " in <1s\n\n    compiling " + app + "\n"
		if !strings.Contains(out, block) {
			t.Errorf("output = %q, want %s's block %q", out, app, block)
		}
	}
	if web, api := strings.Index(out, "✓ web: Built"), strings.Index(out, "✓ api: Built"); web < 0 || api < web {
		t.Errorf("output = %q, want web's block before api's, in the order they finished", out)
	}
}

func TestABuilderFailureOutsideEveryAppsBuildEndsASpanOfItsOwnHoldingWhatTheBuilderSaid(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "Error: Cannot find module 'esbuild'\n")
		return build.Output{}, errors.New("node-builder failed (exit status 1): Error: Cannot find module 'esbuild'")
	}

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy succeeded, want the builder's failure")
	}

	scopes, phaseOutput := buildScopes(t, out)
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Building 2 apps (web and api)" || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("the last build span = %s: %q ended %s, want %s: \"Building 2 apps (web and api)\" ended in error", last.subject, last.message, last.status, clitest.FixtureSlug)
	}
	if strings.Join(last.output, "\n") != "Error: Cannot find module 'esbuild'" {
		t.Errorf("the failed span's output = %q, want what the builder said", last.output)
	}
	if len(phaseOutput) != 0 {
		t.Errorf("build phase output = %q, want none: the builder's words belong to the span that failed", phaseOutput)
	}
}

func TestAnAppsOwnBuildFailureEndsNoSecondSpan(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.BuildApps = buildingEach("web")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy succeeded, want web's build failure")
	}

	scopes, phaseOutput := buildScopes(t, out)
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+" "+scope.status.String())
	}
	want := []string{clitest.FixtureSlug + " SPAN_STATUS_OK", "web SPAN_STATUS_ERROR"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("build spans = %q, want %q: web's span already reports the failure", got, want)
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what the builder said before web's build", phaseOutput)
	}
}

func TestAFailureAssemblingTheManifestAfterTheBuildsEndsASpanOfItsOwn(t *testing.T) {
	dependencies, fixture := twoAppProject(t)
	dependencies.BuildApps = buildingEach("")
	dependencies.DeploymentID = func(string, string) (string, error) {
		return "", errors.New("no deployment id for app \"web\"; run `ocel build`")
	}

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy succeeded, want the manifest's failure")
	}

	scopes, _ := buildScopes(t, out)
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Assembling the deploy manifest of "+clitest.FixtureSlug || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("the last build span = %s: %q ended %s, want the manifest's span ended in error", last.subject, last.message, last.status)
	}
}

func publishBinding(t *testing.T, fixture clitest.FakeProject, binding *bindingsv1.Binding) {
	t.Helper()
	const owner = "infra-repo"
	pair, err := variablestoreserver.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	if _, err := valueStore(fixture).SetBindings(context.Background(), scope, "", owner, []variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
		t.Fatalf("publish %s: %v", binding.GetName(), err)
	}
}

func publishedPostgres(name string) *bindingsv1.Binding {
	return &bindingsv1.Binding{
		Name:   name,
		Source: "infra-repo",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "db.example", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
		}},
	}
}

func deployBound(t *testing.T, bindings string, publish func(clitest.FakeProject)) (clitest.FakeProject, string, error) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, "  bindings: {"+bindings+"},\n")
	if publish != nil {
		publish(fixture)
	}
	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	return fixture, out, err
}

func manifestResource(t *testing.T, manifest *contractv1.Manifest, logicalName string) *contractv1.ManifestResource {
	t.Helper()
	for _, resource := range manifest.GetResources() {
		if resource.GetLogicalName() == logicalName {
			return resource
		}
	}
	t.Fatalf("the manifest has no resource %q among %v", logicalName, manifest.GetResources())
	return nil
}

func usagesOf(manifest *contractv1.Manifest, app string) []string {
	var reached []string
	for _, usage := range manifest.GetUsages() {
		if usage.GetApp() == app {
			reached = append(reached, usage.GetResource())
		}
	}
	slices.Sort(reached)
	return reached
}

func TestDeployBindsListedBindings(t *testing.T) {
	t.Run("a listed resource reaches the provider bound to its published record", func(t *testing.T) {
		fixture, out, err := deployBound(t, `postgres: { main: "@main" }`, func(fixture clitest.FakeProject) {
			publishBinding(t, fixture, publishedPostgres("main"))
		})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		manifest := sentDeploy(t, fixture).GetManifest()
		if bound := manifestResource(t, manifest, "db--main").GetBinding(); bound != "main" {
			t.Errorf("db--main is bound to %q, want the published record main", bound)
		}
		if reached := usagesOf(manifest, "api"); !slices.Equal(reached, []string{"db--main"}) {
			t.Errorf("api reaches %v, want a bound resource to have its usage edge like any other", reached)
		}
	})

	t.Run("a listed resource nothing published refuses the deploy by name", func(t *testing.T) {
		_, out, err := deployBound(t, `postgres: { main: "@main" }`, nil)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; output=%s", out)
		}
		if !strings.Contains(out, "`bindings` binds \"main\", and nothing has published a record under that name") {
			t.Errorf("output = %q, want the refusal to name the binding that was never published", out)
		}
	})

	t.Run("a published name this project provisions instead is called out", func(t *testing.T) {
		_, out, err := deployBound(t, ``, func(fixture clitest.FakeProject) {
			publishBinding(t, fixture, publishedPostgres("main"))
		})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, `A binding named "main" is already published`) {
			t.Errorf("output = %q, want the collision between a provisioned resource and a published binding surfaced", out)
		}
	})

	t.Run("a listed name nothing declares refuses before any provider is reached", func(t *testing.T) {
		fixture, out, err := deployBound(t, `postgres: { nowhere: "@nowhere" }`, nil)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; output=%s", out)
		}
		if !strings.Contains(out, "nowhere") {
			t.Errorf("output = %q, want the unbound binding named", out)
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want the refusal before any", len(sent))
		}
	})
}

func inlineURL(t *testing.T) string {
	t.Helper()
	return "postgres://app:s3cret-pw@" + clitest.ServePostgres(t, "170004") + "/main?sslmode=disable"
}

const inlineByURL = `postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } }`

func deployInline(t *testing.T, fixture clitest.FakeProject, bindings string, opts deployOptions) (string, error) {
	t.Helper()
	writeUsageMonorepo(t, fixture.Root, "  bindings: {"+bindings+"},\n")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	opts.yes = true
	return deployWith(t, dependencies, fixture, opts)
}

func carriedNames(carried []*bindingsv1.Binding) []string {
	names := make([]string, 0, len(carried))
	for _, binding := range carried {
		names = append(names, binding.GetName()+" from "+binding.GetSource())
	}
	return names
}

func TestDeployBindsAnInlineRecord(t *testing.T) {
	t.Run("the request carries the record whole and the manifest names it, never its secret", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		url := inlineURL(t)
		envSet(t, fixture, "MAIN_DATABASE_URL", url, envOptions{})

		out, err := deployInline(t, fixture, inlineByURL, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		req := sentDeploy(t, fixture)
		if bound := manifestResource(t, req.GetManifest(), "db--main").GetBinding(); bound != "ocel:postgres.main" {
			t.Errorf("db--main is bound to %q, want the record the request carries", bound)
		}
		carried := req.GetInlineBindings()
		if len(carried) != 1 || carried[0].GetName() != "ocel:postgres.main" || carried[0].GetSource() != "ocel.config.ts" {
			t.Fatalf("inline bindings = %v, want the one record, sourced from the config", carriedNames(carried))
		}
		if carried[0].GetPostgres().GetUrl() != url {
			t.Error("the carried record lost the url the app connects with")
		}
		manifest, err := protojson.Marshal(req.GetManifest())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(manifest), "s3cret-pw") {
			t.Errorf("the manifest contains the database password: %s", manifest)
		}
		if strings.Contains(out, "s3cret-pw") {
			t.Errorf("the deploy printed the database password: %s", out)
		}
	})

	t.Run("a variable the binding reads and nobody set refuses the deploy with the command that sets it", func(t *testing.T) {
		fixture := setUpDeployProject(t)

		out, err := deployInline(t, fixture, inlineByURL, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded with MAIN_DATABASE_URL unset\n%s", out)
		}
		if said := err.Error() + out; !strings.Contains(said, "ocel env set MAIN_DATABASE_URL=<VALUE>") {
			t.Errorf("refusal = %q, want the command that sets it", said)
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Error("the deploy reached the provider without the record's value")
		}
	})

	t.Run("a dry run hands the provider the record to plan with", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		url := inlineURL(t)
		envSet(t, fixture, "MAIN_DATABASE_URL", url, envOptions{})

		if out, err := deployInline(t, fixture, inlineByURL, deployOptions{dry: true}); err != nil {
			t.Fatalf("dry deploy: %v\n%s", err, out)
		}
		req := sentDeploy(t, fixture)
		if !req.GetDry() || len(req.GetInlineBindings()) != 1 {
			t.Errorf("request dry = %v with %d inline bindings, want the dry plan to carry the record", req.GetDry(), len(req.GetInlineBindings()))
		}
	})

	t.Run("dropping the binding leaves the request carrying no record", func(t *testing.T) {
		fixture := setUpDeployProject(t)
		url := inlineURL(t)
		envSet(t, fixture, "MAIN_DATABASE_URL", url, envOptions{})
		if out, err := deployInline(t, fixture, inlineByURL, deployOptions{}); err != nil {
			t.Fatalf("first deploy: %v\n%s", err, out)
		}

		if out, err := deployInline(t, fixture, ``, deployOptions{}); err != nil {
			t.Fatalf("second deploy: %v\n%s", err, out)
		}
		sent := sentDeploys(t, fixture)
		if carried := sent[len(sent)-1].GetInlineBindings(); len(carried) != 0 {
			t.Errorf("inline bindings = %v, want none: the provider prunes what the request no longer carries", carriedNames(carried))
		}
	})
}

const inlineBucket = `bucket: { uploads: {
  endpoint: "https://abc.storage.example.com", region: "auto", bucket: "acme", prefix: "uploads/",
  accessKeyId: { $env: "BUCKET_KEY" }, secretAccessKey: { $env: "BUCKET_SECRET" },
} }`

func declareUploads(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const files = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, files } from "../../../shared/index.js";

export function handler() {
  return db.name + files.name;
}
`)
}

func TestDeployBindsAnInlineBucket(t *testing.T) {
	fixture := setUpDeployProject(t)
	envSet(t, fixture, "BUCKET_KEY", "AKIDEXAMPLE", envOptions{})
	envSet(t, fixture, "BUCKET_SECRET", "bucket-s3cret", envOptions{})
	writeUsageMonorepo(t, fixture.Root, "  bindings: {"+inlineBucket+"},\n")
	declareUploads(t, fixture.Root)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	carried := sentDeploy(t, fixture).GetInlineBindings()
	if len(carried) != 1 || carried[0].GetName() != "ocel:bucket.uploads" {
		t.Fatalf("inline bindings = %v, want the inline bucket's record", carriedNames(carried))
	}
	want := &bindingsv1.BucketProperties{
		Endpoint: "https://abc.storage.example.com", Region: "auto", Bucket: "acme", Prefix: "uploads/",
		AccessKeyId: "AKIDEXAMPLE", SecretAccessKey: "bucket-s3cret",
	}
	if got := carried[0].GetBucket(); !proto.Equal(got, want) {
		t.Errorf("bucket = %s at %s under %q, want %s at %s under %q with the key pair the variables hold",
			got.GetBucket(), got.GetEndpoint(), got.GetPrefix(), want.GetBucket(), want.GetEndpoint(), want.GetPrefix())
	}
	if strings.Contains(out, "bucket-s3cret") {
		t.Errorf("the deploy printed the store's secret key: %s", out)
	}
}

func TestDeployLandsAUsageEdgeForEveryResourceAnAppReaches(t *testing.T) {
	t.Run("an app that uses a shared resource lands a usage edge naming the files it reaches through", func(t *testing.T) {
		fixture, out, err := deployUsageMonorepo(t, "")
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		usages := sentDeploy(t, fixture).GetManifest().GetUsages()
		if len(usages) != 1 || usages[0].GetApp() != "api" || usages[0].GetResource() != "db--main" || !slices.Equal(usages[0].GetFiles(), []string{"apps/api/src/server.ts"}) {
			t.Errorf("usages = %v, want api reaching db--main through apps/api/src/server.ts", usages)
		}
	})

	t.Run("a resource no app uses still provisions and has no edge", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("output = %q, want the orphan resource to deploy", out)
		}
		if usages := sentDeploy(t, fixture).GetManifest().GetUsages(); len(usages) != 0 {
			t.Errorf("usages = %v, want no usage edge for an orphan resource", usages)
		}
	})

	t.Run("a runtime-computed import in an app fails the deploy closed", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)
		writeUsageMonorepo(t, fixture.Root, "")
		clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "late.ts"), `
const spec = "../../../shared/" + ["d", "b"].join("") + ".js";

export async function late() {
  return await import(spec);
}
`)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; output=%s", out)
		}
		if !strings.Contains(out, "apps/api/src/late.ts") {
			t.Errorf("output = %q, want it to name the file containing the unresolvable import", out)
		}
	})
}

func writeSharedResourceMonorepo(t *testing.T, root string) {
	t.Helper()

	clitest.WriteUsageMonorepo(t, root)
	writeAppsConfig(t, root, `
    { name: "api", path: "apps/api", framework: "node" },
    { name: "web", path: "apps/web", framework: "node" },
  `)
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const uploads = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, uploads } from "../../../shared/index.js";

export function handler() {
  return db.name + uploads.name;
}
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}

func TestDeployScopesDeliveryToTheUsingApps(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
		{Route: "web", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
	})
	fixture := setUpDeployProject(t)
	writeSharedResourceMonorepo(t, fixture.Root)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	manifest := sentDeploy(t, fixture).GetManifest()
	if reached := usagesOf(manifest, "api"); !slices.Equal(reached, []string{"bucket--uploads", "db--main"}) {
		t.Errorf("api reaches %v, want the bucket and the database", reached)
	}
	if reached := usagesOf(manifest, "web"); !slices.Equal(reached, []string{"db--main"}) {
		t.Errorf("web reaches %v: web never reaches the bucket, so it receives neither its values nor its live keys", reached)
	}
}

func TestDeployAttributesAnUnconfiguredProjectToItsOnlyApp(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output", App: clitest.FixtureSlug},
	})
	fixture := setUpDeployProject(t)
	writeRootApp(t, fixture.Root)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	if reached := usagesOf(sentDeploy(t, fixture).GetManifest(), clitest.FixtureSlug); !slices.Equal(reached, []string{"db--main"}) {
		t.Errorf("%s reaches %v, want the only app of a project that configures none to still reach what it declares", clitest.FixtureSlug, reached)
	}
}

func TestDeployRefusesWhatItCannotAttribute(t *testing.T) {
	t.Run("a project that builds two apps and names neither", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
		})
		fixture := setUpDeployProject(t)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; output=%s", out)
		}
		combined := out + err.Error()
		for _, want := range []string{"api", "web", "ocel.config.ts"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})

	t.Run("a built app the config names nothing of", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/legacy", App: "legacy"},
		})
		fixture := setUpDeployProject(t)
		writeUsageMonorepo(t, fixture.Root, "")

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the app no configured app covers to refuse the deploy; output=%s", out)
		}
		if combined := out + err.Error(); !strings.Contains(combined, "legacy") {
			t.Errorf("output = %q, want it to name the app the config covers with nothing", combined)
		}
	})

	t.Run("a configured path that names no directory", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpDeployProject(t)
		clitest.WriteUsageMonorepo(t, fixture.Root)
		writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/ap1", framework: "node" }`)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatalf("runDeploy err = nil, want a path naming nothing to refuse the deploy rather than ship an app no resource reaches; output=%s", out)
		}
		combined := out + err.Error()
		for _, want := range []string{`"api"`, "apps/ap1"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})
}

func productionHostnames(app *contractv1.ManifestApp) []string {
	for _, domains := range app.GetDomains() {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			return domains.GetHostnames()
		}
	}
	return nil
}

func TestADeployBuildsWithItsSensitiveAndSecretValuesOutsideTheBuildEnvironment(t *testing.T) {
	fixture := setUpVariablesProject(t, `[
  {"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true},
  {"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},
  {"key":"SESSION_SECRET","class":"VARIABLE_CLASS_SECRET","required":true}
]`)
	writeRootApp(t, fixture.Root)
	writeAppsConfig(t, fixture.Root, `{ name: "web", path: ".", framework: "next" }`)
	envSet(t, fixture, "PAGE_ID", "page-123", envOptions{})
	envSet(t, fixture, "STRIPE_API_KEY", "sk_live_sensitive", envOptions{})
	envSet(t, fixture, "SESSION_SECRET", "ss_live_secret", envOptions{})

	dependencies := newTestDependencies()
	got := captureBuildVariables(&dependencies)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}

	built := (*got)["web"]
	if built.Env["PAGE_ID"] != "page-123" {
		t.Errorf("build env = %v, want the plaintext PAGE_ID in it", built.Env)
	}
	for key, want := range map[string]string{"STRIPE_API_KEY": "sk_live_sensitive", "SESSION_SECRET": "ss_live_secret"} {
		if built.Live[key] != want {
			t.Errorf("build live values hold %s = %q, want %q", key, built.Live[key], want)
		}
		if _, ok := built.Env[key]; ok {
			t.Errorf("build env holds %s, want an encrypted class only in the live dir", key)
		}
	}
	sent, err := proto.Marshal(sentDeploy(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sent, []byte("ss_live_secret")) {
		t.Error("the deploy request holds the secret's plaintext, want a secret revealed for the build only")
	}
}

func TestADeployRevealsSecretsOnlyForTheAppsWhoseBuildReadsThem(t *testing.T) {
	fixture := setUpVariablesProject(t, `[
  {"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},
  {"key":"SESSION_SECRET","class":"VARIABLE_CLASS_SECRET","required":true}
]`)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "web", "package.json"), "{}\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "package.json"), "{}\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "jobs", "Cargo.toml"), "[package]\nname = \"jobs\"\nversion = \"0.1.0\"\n\n[workspace]\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "jobs", "src", "main.rs"), "fn main() {}\n")
	writeAppsConfig(t, fixture.Root, `{ name: "web", path: "apps/web", framework: "next" }, { name: "api", path: "apps/api", framework: "node" }, { name: "jobs", path: "apps/jobs", framework: "rust" }`)
	envSet(t, fixture, "STRIPE_API_KEY", "sk_live_sensitive", envOptions{})
	envSet(t, fixture, "SESSION_SECRET", "ss_live_secret", envOptions{})

	dependencies := newTestDependencies()
	got := captureBuildVariables(&dependencies)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}

	for _, app := range []string{"web", "jobs"} {
		if (*got)[app].Live["SESSION_SECRET"] != "ss_live_secret" {
			t.Errorf("%s's build live values = %v, want the secret a build that runs the app's code reads", app, (*got)[app].Live)
		}
	}
	if secret, ok := (*got)["api"].Live["SESSION_SECRET"]; ok {
		t.Errorf("api's build was handed SESSION_SECRET = %q, want a secret revealed only for a build that runs the app's code", secret)
	}
}

func TestADeployThatCannotRevealASecretStopsBeforeItProvisionsAnything(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"SESSION_SECRET","class":"VARIABLE_CLASS_SECRET","required":true}]`)
	writeRootApp(t, fixture.Root)
	writeAppsConfig(t, fixture.Root, `{ name: "web", path: ".", framework: "next" }`)
	envSet(t, fixture, "SESSION_SECRET", "ss_live_secret", envOptions{})
	fixture.Provider.Cipher().(*fake.Cipher).RefuseOpening(errors.New("the deployer may not decrypt"))

	dependencies := newTestDependencies()
	captureBuildVariables(&dependencies)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatalf("runDeploy err = nil, want a secret it cannot reveal to stop the deploy; output=%s", out)
	}
	if slices.Contains(fixture.Requests.Procedures(), contractv1connect.ProviderServiceProvisionInfraProcedure) {
		t.Error("ProvisionInfra ran before the deploy found it cannot reveal the build's secrets, want the deploy to stop before it changes the cloud")
	}
}

func TestADeployWarnsThatAnImageBuildGetsNoneOfTheValuesItsAppDeclares(t *testing.T) {
	fixture := setUpVariablesProject(t, `[
  {"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true},
  {"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},
  {"key":"SESSION_SECRET","class":"VARIABLE_CLASS_SECRET","required":true}
]`)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "web", "package.json"), "{}\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "web", "next.config.mjs"), "export default {}\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "package.json"), "{}\n")
	writeAppsConfig(t, fixture.Root, `{ name: "web", path: "apps/web", compute: "container" }, { name: "api", path: "apps/api", compute: "container" }`)
	envSet(t, fixture, "PAGE_ID", "page-123", envOptions{})
	envSet(t, fixture, "STRIPE_API_KEY", "sk_live_sensitive", envOptions{})
	envSet(t, fixture, "SESSION_SECRET", "ss_live_secret", envOptions{})

	dependencies := newTestDependencies()
	captureBuildVariables(&dependencies)
	stubAppImages(&dependencies, "web", "api")
	clitest.ServeImageDaemon(t, "amd64")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	for _, app := range []string{"web", "api"} {
		want := `app "` + app + `" builds as an image, and an image build gets none of its values yet: PAGE_ID, SESSION_SECRET, STRIPE_API_KEY.`
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestADeployWarnsThatAnImageBuildGetsNoneOfItsValuesWhenEveryOneIsPlaintext(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "package.json"), "{}\n")
	writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api", compute: "container" }`)
	envSet(t, fixture, "PAGE_ID", "page-123", envOptions{})

	dependencies := newTestDependencies()
	captureBuildVariables(&dependencies)
	stubAppImages(&dependencies, "api")
	clitest.ServeImageDaemon(t, "amd64")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	if want := `app "api" builds as an image, and an image build gets none of its values yet: PAGE_ID.`; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, processenv.AppURLEnvVar+",") || strings.Contains(out, ", "+processenv.AppURLEnvVar) {
		t.Errorf("the warning names %s, which ocel sets and the app never declares:\n%s", processenv.AppURLEnvVar, out)
	}
}
