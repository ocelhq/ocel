package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands/env"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	clitest.UnsetColorEnv()
	os.Unsetenv("REGISTRY_TOKEN")
	clitest.UnsetGitEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{
		Invocation:              clitest.NewInvocation(),
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		DeploymentID:            build.DeploymentID,
		CollectDeclarations:     declaration.Collect,
		ServeVariableEditor:     env.ServeVariableEditor,
		DiscoverPRNumber:        func() string { return os.Getenv(PRNumberEnvVar) },
	}
}

const productionDomain = "app.acme.com"

func setUpDeployProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	fixture := clitest.SetUpProject(t)
	bootstrapTier(t, fixture, environment.TierProduction)
	writeConfig(t, fixture.Root, "")
	return fixture
}

func setUpPreviewProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	fixture := setUpDeployProject(t)
	bootstrapTier(t, fixture, environment.TierPreview)
	return fixture
}

func bootstrapTier(t *testing.T, fixture clitest.FakeProject, tier environment.Tier) {
	t.Helper()
	clitest.Bootstrap(t, fixture.Provider, tier, fake.FeatureCache, fake.FeatureImages)
}

func removeBootstrap(t *testing.T, fixture clitest.FakeProject, tier environment.Tier) {
	t.Helper()
	if err := fixture.Provider.FakeBootstrap().Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("remove the %s bootstrap: %v", tier, err)
	}
}

func writeConfig(t *testing.T, root, fields string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { production: "`+productionDomain+`", preview: "*.preview.acme.com" },
`+fields+`};
`)
}

func writeAppsConfig(t *testing.T, root, apps string) {
	t.Helper()
	writeConfig(t, root, "  apps: ["+apps+"],\n")
}

func addAppToFixtureConfig(t *testing.T, root string) {
	t.Helper()
	writeAppsConfig(t, root, `{ name: "api", path: "apps/api", framework: "node" }`)
	writeAppSource(t, root, "api")
}

func writeAppSource(t *testing.T, root string, apps ...string) {
	t.Helper()
	for _, app := range apps {
		clitest.WriteFile(t, filepath.Join(root, "apps", app, "src", "server.ts"), `
export function handler() {
  return "`+app+`";
}
`)
	}
}

func writeRootApp(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")
}

func writeUsageMonorepo(t *testing.T, root, fields string) {
	t.Helper()
	clitest.WriteUsageMonorepo(t, root)
	writeConfig(t, root, `  apps: [{ name: "api", path: "apps/api", framework: "node" }],
`+fields)
}

func apiFunction() []build.Function {
	return []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	}
}

func stubBuild(dependencies *Dependencies, functions []build.Function) {
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.HostedWorkers, _ build.Log) (build.Output, error) {
		return build.Output{Functions: functions}, writeArtifacts(cfg.Dir, functions)
	}
	dependencies.ReadPrebuilt = func(_ context.Context, cfg *project.Project, _ map[string]string) (build.Output, error) {
		return build.Output{Functions: functions}, writeArtifacts(cfg.Dir, functions)
	}
	stubRecordedDeploymentIDs(dependencies)
}

func writeArtifacts(projectDir string, functions []build.Function) error {
	root, err := buildoutput.Root(projectDir)
	if err != nil {
		return err
	}
	for _, function := range functions {
		dir := filepath.Join(root, filepath.FromSlash(function.ArtifactPath))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("export const handler = () => \""+function.App+"\";\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func stubAppImages(dependencies *Dependencies, apps ...string) {
	refs := make(map[string]string, len(apps))
	for _, app := range apps {
		refs[app] = clitest.FixtureImage(app)
	}
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error {
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, workers build.HostedWorkers, log build.Log) (build.Output, error) {
		built, err := buildApps(ctx, cfg, env, archs, workers, log)
		built.Images = refs
		return built, err
	}
	readPrebuilt := dependencies.ReadPrebuilt
	dependencies.ReadPrebuilt = func(ctx context.Context, cfg *project.Project, archs map[string]string) (build.Output, error) {
		built, err := readPrebuilt(ctx, cfg, archs)
		built.Images = refs
		return built, err
	}
}

func stubRecordedDeploymentIDs(dependencies *Dependencies) {
	dependencies.DeploymentID = func(_, app string) (string, error) { return recordedDeploymentID(app), nil }
}

func recordedDeploymentID(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-deployment/" + app))
	return hex.EncodeToString(sum[:])[:32]
}

func useJSONFormat(t *testing.T, dependencies *Dependencies) {
	t.Helper()
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}
}

func terminalStdin(dependencies *Dependencies) {
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
}

func stubGit(dependencies *Dependencies, branch, pr string) {
	dependencies.ReadGitBranch = func(string) (string, error) { return branch, nil }
	dependencies.DiscoverPRNumber = func() string { return pr }
}

func envelopes(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var events []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func sentDeploys(t *testing.T, fixture clitest.FakeProject) []*contractv1.DeployRequest {
	t.Helper()
	return clitest.RequestsTo[*contractv1.DeployRequest](t, fixture.Requests, contractv1connect.ProviderServiceDeployProcedure)
}

func sentDeploy(t *testing.T, fixture clitest.FakeProject) *contractv1.DeployRequest {
	t.Helper()
	sent := sentDeploys(t, fixture)
	if len(sent) != 1 {
		t.Fatalf("the CLI sent %d deploys, want exactly 1", len(sent))
	}
	return sent[0]
}

func sentPreflights(t *testing.T, fixture clitest.FakeProject) []*contractv1.PreflightRequest {
	t.Helper()
	return clitest.RequestsTo[*contractv1.PreflightRequest](t, fixture.Requests, contractv1connect.ProviderServicePreflightProcedure)
}

func manifestApp(t *testing.T, manifest *contractv1.Manifest, name string) *contractv1.ManifestApp {
	t.Helper()
	for _, app := range manifest.GetApps() {
		if app.GetName() == name {
			return app
		}
	}
	t.Fatalf("the manifest has no app %q among %d apps", name, len(manifest.GetApps()))
	return nil
}

func recordProjects(t *testing.T, fixture clitest.FakeProject, tier environment.Tier, slugs ...string) {
	t.Helper()
	for _, slug := range slugs {
		body, err := json.Marshal(stackrecords.Project{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.Provider.KeyValues().Write(context.Background(), keyvalue.Entry{Key: stackrecords.ProjectKey(tier, slug), Value: body}); err != nil {
			t.Fatalf("record project %s: %v", slug, err)
		}
	}
}

func setUpVariablesProject(t *testing.T, definitions string) clitest.FakeProject {
	t.Helper()
	return setUpVariablesProjectWith(t, definitions, clitest.EnvDeclarationScript)
}

func setUpVariablesProjectWith(t *testing.T, definitions, script string) clitest.FakeProject {
	t.Helper()
	fixture := setUpDeployProject(t)
	t.Setenv("OCEL_TEST_ENV_DEFINITIONS", definitions)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS", "[]")
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(fixture.Root), "env.ts"), script)
	return fixture
}

type envOptions struct {
	folder      string
	environment string
	preview     bool
}

func (o envOptions) tier() environment.Tier {
	if o.preview {
		return environment.TierPreview
	}
	return environment.TierProduction
}

func valueStore(fixture clitest.FakeProject) variablestore.Store {
	return variablestore.Store{KeyValues: fixture.Provider.KeyValues(), Cipher: fixture.Provider.Cipher()}
}

func envSet(t *testing.T, fixture clitest.FakeProject, key, value string, opts envOptions) {
	t.Helper()
	setValue(t, fixture, variablestore.Scope{Project: clitest.FixtureSlug, Tier: opts.tier()}, key, value, opts)
}

func setValue(t *testing.T, fixture clitest.FakeProject, scope variablestore.Scope, key, value string, opts envOptions) {
	t.Helper()
	at := variablestore.Coordinate{Cell: variablestore.Cell{Folder: opts.folder, Key: key}, Environment: opts.environment}
	if _, err := valueStore(fixture).Set(context.Background(), scope, at, value, nil); err != nil {
		t.Fatalf("set %s for %s: %v", key, scope.Project, err)
	}
}
