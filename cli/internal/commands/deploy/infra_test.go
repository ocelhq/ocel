package deploy

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func sentProvisionInfras(t *testing.T, fixture clitest.FakeProject) []*contractv1.ProvisionInfraRequest {
	t.Helper()
	return clitest.RequestsTo[*contractv1.ProvisionInfraRequest](t, fixture.Requests, contractv1connect.ProviderServiceProvisionInfraProcedure)
}

func deployed(t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, opts deployOptions) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func TestADeployProvisionsInfraBeforeItBuildsThenDeploysOverIt(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	var provisionedBeforeBuild bool
	built := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		provisionedBeforeBuild = slices.Contains(fixture.Requests.Procedures(), contractv1connect.ProviderServiceProvisionInfraProcedure)
		return built(ctx, cfg, variables, archs, workers, host, log)
	}

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if !provisionedBeforeBuild {
		t.Error("the apps were built before ProvisionInfra, so a build that reads a resource finds nothing provisioned")
	}
	infra := sentProvisionInfras(t, fixture)
	if len(infra) != 1 {
		t.Fatalf("the CLI sent %d ProvisionInfra requests, want exactly 1", len(infra))
	}
	if apps, resources := infra[0].GetManifest().GetApps(), infra[0].GetManifest().GetResources(); len(apps) != 0 || len(resources) != 1 {
		t.Errorf("ProvisionInfra was sent %d apps and %d resources, want no app and the one resource the project declares", len(apps), len(resources))
	}
	if !sentDeploy(t, fixture).GetInfraProvisioned() {
		t.Error("the deploy did not say its infra was provisioned, so the provider provisions it a second time")
	}
}

func TestADryDeployPlansInfraWithItsApps(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{dry: true})

	if sent := sentProvisionInfras(t, fixture); len(sent) != 0 {
		t.Errorf("a dry deploy sent %d ProvisionInfra requests, want none: it changes nothing", len(sent))
	}
}

func TestAPrebuiltDeployLeavesInfraToTheDeploy(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{yes: true, prebuilt: true})

	if sent := sentProvisionInfras(t, fixture); len(sent) != 0 {
		t.Errorf("a --prebuilt deploy sent %d ProvisionInfra requests, want none: nothing builds, so the deploy provisions infra itself", len(sent))
	}
	if sentDeploy(t, fixture).GetInfraProvisioned() {
		t.Error("a --prebuilt deploy said its infra was provisioned, and nothing provisioned it")
	}
}

func TestAnEphemeralPreviewProvisionsNoInfra(t *testing.T) {
	fixture := setUpPreviewProject(t)

	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{})

	if sent := sentProvisionInfras(t, fixture); len(sent) != 0 {
		t.Errorf("an ephemeral preview sent %d ProvisionInfra requests, want none: it has no infra stack", len(sent))
	}
	if sentDeploy(t, fixture).GetInfraProvisioned() {
		t.Error("an ephemeral preview said its infra was provisioned, and it has none")
	}
}

func TestAPersistentPreviewProvisionsItsInfraBeforeItBuilds(t *testing.T) {
	fixture := setUpPreviewProject(t)

	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})

	infra := sentProvisionInfras(t, fixture)
	if len(infra) != 1 || infra[0].GetEnvironment().GetIdentity() != "staging" {
		t.Fatalf("the CLI sent %d ProvisionInfra requests, want one for preview staging", len(infra))
	}
	deployed := sentDeploy(t, fixture)
	if !deployed.GetInfraProvisioned() {
		t.Error("the preview deploy did not say its infra was provisioned")
	}
	if alias := infra[0].GetAliasToken(); alias == "" || alias != deployed.GetAliasToken() {
		t.Errorf("ProvisionInfra was sent the alias token %q and Deploy %q, want the one alias the build was given in both: the provider records it with the preview",
			alias, deployed.GetAliasToken())
	}
}

func TestInfraProvisionedInARunIsNotProvisionedAgainForTheSameResources(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	ctx := context.Background()
	policy, cfg, err := ensureProject(ctx, dependencies, "ocel deploy", fixture.Root, true, false, &stdout, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	orders := declaration.Resource{Name: "orders", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "src/db.ts:1"}
	uploads := declaration.Resource{Name: "uploads", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Bucket: &resourcesv1.BucketConfig{}, Source: "src/files.ts:1"}

	err = dependencies.WithProvider(ctx, cfg, "ocel deploy", productionOpenOptions(policy, cfg), func(ctx context.Context, p commands.ProviderRun) error {
		env := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
		infra := newInfraProvisioning(p.Provider, env, preflightFacts{project: p.Project}, false, false)
		for _, resources := range [][]declaration.Resource{{orders}, {orders}, {orders, uploads}} {
			if err := infra.provision(ctx, resources, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("provision() error = %v; stdout=%s", err, stdout.String())
	}

	if sent := sentProvisionInfras(t, fixture); len(sent) != 2 {
		t.Errorf("the CLI sent %d ProvisionInfra requests, want 2: one for orders, none again for the same orders, one once uploads is declared", len(sent))
	}
}

func TestADeployHoldsTheEnvironmentUnderOneLeaseFromItsInfraToItsApps(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	infra := sentProvisionInfras(t, fixture)
	if len(infra) != 1 {
		t.Fatalf("the CLI sent %d ProvisionInfra requests, want exactly 1", len(infra))
	}
	token := infra[0].GetLeaseToken()
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(token) {
		t.Errorf("ProvisionInfra was sent the lease token %q, want 32 lowercase hex digits", token)
	}
	if got := sentDeploy(t, fixture).GetLeaseToken(); got != token {
		t.Errorf("Deploy was sent the lease token %q, want %q, the one that provisioned the infra: the deploy ships under the lease it provisioned under", got, token)
	}
}

func TestADeployNeverReusesALeaseTokenAcrossRuns(t *testing.T) {
	seen := map[string]bool{}
	for _, run := range []string{"first", "second"} {
		t.Run(run, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			fixture := setUpDeployProject(t)

			deployed(t, dependencies, fixture, deployOptions{yes: true})

			seen[sentProvisionInfras(t, fixture)[0].GetLeaseToken()] = true
		})
	}
	if len(seen) != 2 {
		t.Errorf("two runs sent %d distinct lease tokens, want 2: a token shared between runs would let one run renew another's lease", len(seen))
	}
}

func TestAPrebuiltDeploySendsNoLeaseTokenForTheProviderToMint(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{yes: true, prebuilt: true})

	if got := sentDeploy(t, fixture).GetLeaseToken(); got != "" {
		t.Errorf("a --prebuilt deploy was sent the lease token %q, want none: nothing provisioned infra under one", got)
	}
}

func environmentLeaseHeld(t *testing.T, fixture clitest.FakeProject) bool {
	t.Helper()
	_, err := fixture.Provider.KeyValues().Read(context.Background(), stackrecords.EnvironmentLeaseKey(environment.TierProduction, clitest.FixtureSlug, stackrecords.ProductionEnv))
	if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		t.Fatalf("reading the deploy lease = %v", err)
	}
	return err == nil
}

func TestADeployWhoseBuildFailsAfterItsInfraFreesTheEnvironmentItHeld(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("simulated build failure")
	}
	fixture := setUpDeployProject(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded through a failed build")
	}

	if len(sentProvisionInfras(t, fixture)) != 1 {
		t.Fatal("the build failed before ProvisionInfra ran, so this test holds no lease to free")
	}
	if environmentLeaseHeld(t, fixture) {
		t.Error("the environment still holds the lease of a deploy whose build failed, want it abandoned so the next deploy is not refused until it expires")
	}
}

func TestASucceededDeployLeavesNoLeaseBehind(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if environmentLeaseHeld(t, fixture) {
		t.Error("the environment still holds a deploy lease after the deploy succeeded")
	}
}
