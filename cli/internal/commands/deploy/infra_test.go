package deploy

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
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
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		provisionedBeforeBuild = slices.Contains(fixture.Requests.Procedures(), contractv1connect.ProviderServiceProvisionInfraProcedure)
		return built(ctx, cfg, env, archs, workers, host, log)
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
	if !sentDeploy(t, fixture).GetInfraProvisioned() {
		t.Error("the preview deploy did not say its infra was provisioned")
	}
}
