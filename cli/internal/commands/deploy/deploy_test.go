package deploy

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestDeployRefusesBeforeStartingAProviderWhenTheConfigCannotNameOne(t *testing.T) {
	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		err := runDeploy(context.Background(), newTestDependencies(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("a malformed config errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.config.ts") {
			t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})
}

func TestADeployStreamsTheProvidersProgressAndPromotesProduction(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()

	t.Run("streams the provider's progress", func(t *testing.T) {
		if !strings.Contains(out, "Provisioned stack") {
			t.Errorf("stdout = %q, want the provider's streamed progress", out)
		}
	})
	t.Run("ends on a terminal success message", func(t *testing.T) {
		if !strings.Contains(out, "Deployed test-app to production") {
			t.Errorf("stdout = %q, want a terminal success message", out)
		}
	})
	t.Run("sends a production environment", func(t *testing.T) {
		env := sentDeploy(t, fixture).GetEnvironment()
		if env.GetTier() != environmentv1.Tier_TIER_PRODUCTION || env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED {
			t.Errorf("deploy environment = %v, want production", env)
		}
	})
	t.Run("promotes production", func(t *testing.T) {
		if promoted := activePromotion(t, fixture, environment.TierProduction, router.DefaultPointer); promoted == "" {
			t.Error("production serves no promotion after the deploy")
		}
	})
	t.Run("skips the confirm prompt under --yes", func(t *testing.T) {
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", out)
		}
	})
}

func TestADeployWhoseStdinIsNotATerminalProceedsWithoutPrompting(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
	}
}

func TestADeploysResultNamesTheProjectAndProduction(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	evs := envelopes(t, stream.String())
	if headline := evs[len(evs)-1].GetSummary().GetHeadline(); headline != "Deployed test-app to production" {
		t.Fatalf("result headline = %q, want it to name the project and production", headline)
	}
}

func activePromotion(t *testing.T, fixture clitest.FakeProject, tier environment.Tier, pointer string) string {
	t.Helper()
	promoted, err := fixture.Provider.Releases(tier, clitest.FixtureSlug).ActivePromotionID(context.Background(), pointer)
	if err != nil {
		t.Fatalf("read the promotion %s serves: %v", pointer, err)
	}
	return promoted
}

func TestADeployRecordsWhatItDeployed(t *testing.T) {
	t.Run("a successful deploy records the promotion, the tag and every app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpDeployProject(t)
		writeConfigWithProvider(t, fixture.Root, `dns: { zone: { zone: "acme.com" } }`, `  apps: [{ name: "api", path: "apps/api", compute: { serverless: { framework: "node" } } }],
`)
		writeAppSource(t, fixture.Root, "api")
		writeHosting(t, fixture.Root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, tag: "v9"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployReport(t, fixture.Root)
		if got.GetEnvironment().GetTier() != environmentv1.Tier_TIER_PRODUCTION {
			t.Errorf("environment.tier = %v, want production", got.GetEnvironment().GetTier())
		}
		if got.GetProvider().GetName() != "fake" {
			t.Errorf("provider = %v, want the config's provider", got.GetProvider())
		}
		if want := activePromotion(t, fixture, environment.TierProduction, router.DefaultPointer); want == "" || got.GetPromotion().GetId() != want {
			t.Errorf("promotion = %q, want the %q production now serves", got.GetPromotion().GetId(), want)
		}
		if got.GetPromotion().GetTag() != "v9" {
			t.Errorf("tag = %q, want %q", got.GetPromotion().GetTag(), "v9")
		}
		if len(got.GetApps()) != 1 || got.GetApps()[0].GetName() != "api" || got.GetApps()[0].GetFrameworkBuildId() != "bld_api_1" {
			t.Errorf("apps = %v, want one api app with framework build id bld_api_1", got.GetApps())
		}
		if len(got.GetApps()) == 1 && !slices.Contains(got.GetApps()[0].GetUrls(), "https://"+productionDomain) {
			t.Errorf("apps = %v, want api's URLs to include the hostname it serves on", got.GetApps())
		}
		if got.GetFinishedAt().AsTime().IsZero() {
			t.Error("finishedAt is zero, want the completion time")
		}
	})

	t.Run("a failed deploy leaves no stale result behind", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)
		if err := deployreport.Write(fixture.Root, &consolev1.Deployment{Id: "4bf92f3577b34da6a3ce929d0e0e4736"}); err != nil {
			t.Fatalf("seed stale result: %v", err)
		}
		fixture.Provider.FakeStacks().Entering(func(provider.StackSpec) error { return errors.New("simulated deploy failure") })

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the simulated failure; stdout=%s", stdout.String())
		}

		if _, statErr := os.Stat(deployreport.Path(fixture.Root)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want no result file after a failed deploy", deployreport.Path(fixture.Root), statErr)
		}
	})

	t.Run("a successful preview up records the named preview", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpPreviewProject(t)
		addAppToFixtureConfig(t, fixture.Root)
		writeHosting(t, fixture.Root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{name: "e2e-42", persistent: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployReport(t, fixture.Root)
		if got.GetKind() != consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP || got.GetEnvironment().GetTier() != environmentv1.Tier_TIER_PREVIEW || got.GetEnvironment().GetIdentity() != "e2e-42" {
			t.Errorf("deployment is a %v in %v, want a preview up of the named preview", got.GetKind(), got.GetEnvironment())
		}
		if want := activePromotion(t, fixture, environment.TierPreview, "e2e-42"); want == "" || got.GetPromotion().GetId() != want {
			t.Errorf("promotion = %q, want the %q the preview now serves", got.GetPromotion().GetId(), want)
		}
		if len(got.GetApps()) != 1 || len(got.GetApps()[0].GetUrls()) == 0 {
			t.Errorf("apps = %v, want api with the URLs the preview serves it on", got.GetApps())
		}
	})
}

func writeHosting(t *testing.T, root, app, buildID string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", app, buildoutput.HostingFile),
		`{"version":1,"framework":"node","frameworkBuildId":"`+buildID+`"}`)
}

func setUpProviderProject(t *testing.T, options string, transforms string) (clitest.FakeProject, Dependencies) {
	t.Helper()

	fixture := setUpDeployProject(t)
	clitest.WriteUsageMonorepo(t, fixture.Root)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  transforms: [`+transforms+`],
  provider: { fake: `+options+` },
  domains: { production: "`+productionDomain+`" },
  apps: [{ name: "api", path: "apps/api", compute: { serverless: { framework: "node" } } }],
};
`)

	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	return fixture, dependencies
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	fixture, dependencies := setUpProviderProject(t, `{ region: "zone-b" }`, `"./transforms/net.transform.ts"`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil || !strings.Contains(stdout.String(), `lists "./transforms/net.transform.ts" under "transforms"`) {
		t.Fatalf("runDeploy err = %v; stdout=%s, want the provider, which renders nothing a transform patches, to refuse the transform it was configured with", err, stdout.String())
	}

	configured := clitest.RequestsTo[*contractv1.ConfigureRequest](t, fixture.Requests, contractv1connect.ProviderServiceConfigureProcedure)
	if len(configured) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session", len(configured))
	}
	config := configured[0].GetConfig()
	if region := config.GetOptions().AsMap()["region"]; region != "zone-b" {
		t.Errorf("provider options = %v, want the region the config names", config.GetOptions().AsMap())
	}
	if !slices.Equal(config.GetTransforms(), []string{"./transforms/net.transform.ts"}) {
		t.Errorf("provider transforms = %v, want the one the config names", config.GetTransforms())
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	fixture, dependencies := setUpProviderProject(t, `{ regionn: "zone-b" }`, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want options the provider refuses reported; stdout=%s", stdout.String())
	}
	rendered := stdout.String() + stderr.String()
	for _, want := range []string{
		`configures provider "fake" with options it does not accept`,
		`"provider.fake.regionn"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "invalid_argument:") {
		t.Errorf("rendered output = %q, want no raw connect code prefix", rendered)
	}
}

func deployUsageMonorepo(t *testing.T, providerOptions, fields string) (clitest.FakeProject, string, error) {
	t.Helper()
	fixture := setUpDeployProject(t)
	writeUsageMonorepoWithProvider(t, fixture.Root, providerOptions, fields)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return fixture, stdout.String() + stderr.String(), err
}

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", ""},
		{"a declared direct edge names it", "edge: \"direct\"", "direct"},
		{"a declared relay edge names it", "edge: \"relay\"", "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, out, err := deployUsageMonorepo(t, tc.declaration, "")
			if err != nil {
				t.Fatalf("runDeploy err = %v; output=%s", err, out)
			}
			if got := sentDeploy(t, fixture).GetEdge().GetKind(); got != tc.want {
				t.Errorf("the deploy named edge %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeploySendsTheEdgeSettingsUnchanged(t *testing.T) {
	fixture, out, err := deployUsageMonorepo(t, "edge: \"relay\", dns: { zone: { zone: \"acme.com\" } }", "  allowDegraded: [\"streaming\", \"edge-cache\"],\n")
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}

	sent := sentDeploy(t, fixture).GetEdge()
	if sent.GetDns().GetKind() != string(fake.KindZone) || sent.GetDns().GetZone() != "acme.com" {
		t.Errorf("the deploy named DNS %s/%s, want zone/acme.com", sent.GetDns().GetKind(), sent.GetDns().GetZone())
	}
	if !slices.Equal(sent.GetAllowDegraded(), []string{"streaming", "edge-cache"}) {
		t.Errorf("the deploy allowed %v degraded, want streaming and edge-cache", sent.GetAllowDegraded())
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "relay" edge today; it supports direct`

	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, "")
	fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Refuse(errors.New(refusal))
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the refused edge to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, refusal) {
		t.Errorf("rendered output = %q, want it to include %q", rendered, refusal)
	}
	if strings.Contains(rendered, "connection lost") {
		t.Errorf("rendered output = %q, want a refusal not to read as a lost connection", rendered)
	}
}

func registryProject(t *testing.T, registry string) (Dependencies, clitest.FakeProject, func() bool) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	stubAppImages(&dependencies, "api")
	built := false
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		built = true
		return buildApps(ctx, cfg, variables, archs, workers, host, log)
	}

	fixture := setUpDeployProject(t)
	clitest.ServeImageDaemon(t, "amd64")
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: "container" }],`+registry+"\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, fixture, func() bool { return built }
}

func TestARegistryWhoseVariableIsUnsetStopsTheDeployBeforeAnythingIsBuilt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "")
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() built and deployed a project whose registry password is nowhere to be read, want it refused at the plan")
	}
	said := err.Error() + stdout.String() + stderr.String()
	for _, want := range []string{"OCEL_TEST_REGISTRY_TOKEN", "registry.example.com"} {
		if !strings.Contains(said, want) {
			t.Errorf("runDeploy() failed with %q, want it to mention %q", said, want)
		}
	}
	if built() {
		t.Error("the image was built before the deploy discovered it had nowhere to push it")
	}
	if strings.Contains(stdout.String(), "Loaded the provider") {
		t.Errorf("the deploy loaded the provider before reading the password it needs, want it refused first: %s", stdout.String())
	}
}

func TestARegistryThatRefusesThePushStopsTheDeployAtTheCheckBeforeAnythingIsBuilt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com/acme", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)
	fixture.Provider.ImageStore().DenyPushes(refusal.Refuse(refusal.CodeDenied, "registry.example.com refuses acme-bot a push to registry.example.com/acme/api: DENIED: permission_denied: create_package"))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() deployed with a registry that refuses the push, want it refused at the check")
	}
	said := err.Error() + stdout.String()
	for _, want := range []string{"registry.example.com", "create_package", "Checking push access to registry.example.com/acme failed"} {
		if !strings.Contains(said, want) {
			t.Errorf("runDeploy() said %q, want it to mention %q", said, want)
		}
	}
	if built() {
		t.Error("the image was built before the deploy found the registry refuses the push")
	}
	if probed := fixture.Provider.ImageStore().Probed(); !slices.Equal(probed, []string{"test-app.api"}) {
		t.Errorf("push access probed for %q, want the one container app", probed)
	}
	if strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestARegistryPasswordPastedAsATokenIsRefusedWithoutEchoingIt(t *testing.T) {
	const token = "tok_16C7e42F292c6912E7710c838347Ae178B4a"
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com", password: "`+token+`" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() took a pasted token as the name of an environment variable, and would authenticate as nobody")
	}
	said := err.Error() + stdout.String() + stderr.String()
	if strings.Contains(said, token) {
		t.Errorf("the deploy said %q, and the pasted token reached the terminal, the CI log and anything scraping either", said)
	}
	if !strings.Contains(said, "password") {
		t.Errorf("the deploy said %q, want it to name the field that is wrong", said)
	}
	if built() {
		t.Error("the image was built before the deploy discovered its registry credential was nonsense")
	}
}

func TestARegistryWhoseVariableIsSetDeploysAsUsual(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, fixture, _ := registryProject(t, `
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if said := stdout.String() + stderr.String(); strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestAProjectThatNamesNoRegistryDemandsNoSecret(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func containerProject(t *testing.T, health string) (Dependencies, clitest.FakeProject) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	stubAppImages(&dependencies, "api")

	fixture := setUpDeployProject(t)
	clitest.ServeImageDaemon(t, "amd64")
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: { container: { `+health+` } } }],`+"\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, fixture
}

func deployContainerProject(t *testing.T, health string) *contractv1.ManifestApp {
	t.Helper()

	dependencies, fixture := containerProject(t, health)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return manifestApp(t, sentDeploy(t, fixture).GetManifest(), "api")
}

func TestAContainerAppReachesTheProviderAsOneDigestPinnedProcess(t *testing.T) {
	container := deployContainerProject(t, "").GetContainer()

	if container.GetImage() != clitest.FixtureImage("api") || container.GetHealthCheckPath() != "" {
		t.Errorf("container = %s checked at %q, want %s with no health path — the container reaching the provider pinned at the digest the build produced, and the provider choosing the path for an app that names none", container.GetImage(), container.GetHealthCheckPath(), clitest.FixtureImage("api"))
	}
}

func TestAContainerAppContributesZeroFunctions(t *testing.T) {
	api := deployContainerProject(t, "")

	if functions := api.GetServerless().GetFunctions(); len(functions) != 0 {
		t.Errorf("api carries %d functions, want none at all for an app one always-on process serves: routing collapses to that process, and a packed zip beside it would be a second answer", len(functions))
	}
}

func TestAContainersHealthPathIsTheOneTheAppNames(t *testing.T) {
	container := deployContainerProject(t, `health: { path: "/healthz" }`).GetContainer()

	if container.GetHealthCheckPath() != "/healthz" {
		t.Errorf("health path = %q, want the health path the app names passed through to the provider", container.GetHealthCheckPath())
	}
}

func TestAContainerAppRendersADigestPinnedManifestUnderDry(t *testing.T) {
	dependencies, fixture := containerProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy --dry err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	req := sentDeploy(t, fixture)
	if image := manifestApp(t, req.GetManifest(), "api").GetContainer().GetImage(); !req.GetDry() || image != clitest.FixtureImage("api") {
		t.Errorf("the dry deploy sent dry=%v with image %q, want %q — a dry run plans the manifest a real deploy would send, digest and all", req.GetDry(), image, clitest.FixtureImage("api"))
	}
	if !strings.Contains(stdout.String(), "api  image") {
		t.Errorf("stdout = %q, want the plan to show the image it would push", stdout.String())
	}
}

func TestTheRegistryTheProjectNamesRidesTheDeployWithItsSecretResolved(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, fixture, _ := registryProject(t, `
  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	registry := sentDeploy(t, fixture).GetProjectRegistry()
	if registry.GetServer() != "registry.example.com" || registry.GetNamespace() != "" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "hunter2" {
		t.Errorf("registry = %s as %q in %q, want registry.example.com as acme-bot with its secret resolved — the push is an engine resource, so the registry it pushes to reaches the release", registry.GetServer(), registry.GetUsername(), registry.GetNamespace())
	}
}

func TestADeployThatNamesNoRegistrySendsNone(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if registry := sentDeploy(t, fixture).GetProjectRegistry(); registry != nil {
		t.Errorf("registry = %s, want none on a deploy whose project names none: the provider resolves its own inside the deploy, so its credentials never cross the CLI", registry.GetServer())
	}
}

func TestAServerlessOnlyDeployStillSendsTheRegistryItsFunctionsMayBeRunFrom(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	fixture := setUpDeployProject(t)
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: { serverless: { framework: "node" } } }],
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
`)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if server := sentDeploy(t, fixture).GetProjectRegistry().GetServer(); server != "registry.example.com" {
		t.Errorf("registry = %q, want registry.example.com — a provider that runs its functions from images pushes them somewhere too", server)
	}
}

func TestTheImageIsBuiltForTheArchitectureTheProviderSaysItsContainersRunOn(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")
	fixture.Provider.WrappingContainers("arm64", []byte(fake.RuntimeBinary))
	clitest.ServeImageDaemon(t, "arm64")
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, variables, archs, workers, host, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	want := map[string]string{"api": "arm64"}
	if !maps.Equal(required, want) {
		t.Errorf("the builder was checked for %v, want %v: a daemon that cannot build for the target is refused before anything is provisioned", required, want)
	}
	if !maps.Equal(built, want) {
		t.Errorf("the image was built for %v, want %v, what the provider names for the app: left to this machine's own architecture, the target may not run it", built, want)
	}
}

func TestAnAppThatFallsBackToContainerIsBuiltForTheArchitectureTheProviderNames(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
	fixture.Provider.WrappingContainers("arm64", []byte(fake.RuntimeBinary))
	clitest.ServeImageDaemon(t, "arm64")
	writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api" }`)
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, variables, archs, workers, host, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	want := map[string]string{"api": "arm64"}
	if !maps.Equal(required, want) {
		t.Errorf("the builder was checked for %v, want %v: an app the provider runs in a container is one whether or not the config says so", required, want)
	}
	if !maps.Equal(built, want) {
		t.Errorf("the image was built for %v, want %v, what the provider names for an app that falls back to its container compute", built, want)
	}
}

func TestAPrebuiltDeployDoesNotAskWhetherThisMachineCanBuildImages(t *testing.T) {
	dependencies, fixture, built := registryProject(t, "")
	asked := false
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error {
		asked = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if asked {
		t.Error("a --prebuilt deploy asked whether this machine can build the image it will not build")
	}
	if built() {
		t.Error("a --prebuilt deploy built the app")
	}
}
