package providerserver_test

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const containerTestImage = "ocel/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerDeployRequest(probe string) *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Apps[0].Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		Image:           containerTestImage,
		HealthCheckPath: probe,
	}}
	return req
}

func namingARegistry(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	req.ImageRegistry = &contractv1.ImageRegistry{
		Server:    "ghcr.io",
		Namespace: "acme",
		Username:  "acme-bot",
		Password:  "hunter2",
	}
	return req
}

func TestTheWireAcceptsAnAppNamingContainer(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, _ := deployServed(t)

	req := namingARegistry(containerDeployRequest("/"))

	result, _, err := deployStream(t, client, req)
	if err != nil {
		t.Fatalf("Deploy() with an app naming %q: error = %v, want the wire pin to admit it — it is the only compute the VPS provider runs", provider.ComputeContainer, err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with an app naming %q = %q, want it to succeed", provider.ComputeContainer, result.GetError())
	}
}

func TestTheWireRefusesAnAppThatCarriesNoArtifact(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Artifact = nil

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app carrying neither functions nor an image: code = %v, want %v — the manifest pin is what gives every app a compute by the time a provider reads it", got, connect.CodeInvalidArgument)
	}
}

func TestTheAppSpecNamesTheImageAndProbeAContainerAppIsProvisionedFrom(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/healthz")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last plan the stacks port saw provisions no app")
	}
	if app.Compute != provider.ComputeContainer {
		t.Errorf("Compute = %q, want %q: the primitive is chosen by what the plan names", app.Compute, provider.ComputeContainer)
	}
	if app.Image != pushedCoordinate {
		t.Errorf("Image = %q, want %q: the plan names the coordinate the push wrote, not the ref the build left in the local store", app.Image, pushedCoordinate)
	}
	if app.HealthCheckPath != "/healthz" {
		t.Errorf("HealthCheckPath = %q, want the path the manifest says the process is probed at", app.HealthCheckPath)
	}
}

func TestAServerlessAppSpecNamesItsComputeAndNoImage(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app.Compute != provider.ComputeServerless {
		t.Errorf("Compute = %q, want %q", app.Compute, provider.ComputeServerless)
	}
	if app.Image != "" || app.HealthCheckPath != "" {
		t.Errorf("Image = %q and HealthCheckPath = %q, want a serverless app to have neither", app.Image, app.HealthCheckPath)
	}
}

func TestTheContainerAProvisionedAppRunsOnIsRecordedAgainstItsStack(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), p.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	stacks := 0
	for _, entry := range entries {
		if entry.Kind != provider.StackApp {
			continue
		}
		stacks++
		if len(entry.Containers) != 1 || entry.Containers[0].Name != "web" {
			t.Fatalf("%s recorded %v, want the container it ran the app as: nothing can take down what was never written", entry.Name, entry.Containers)
		}
		if entry.Containers[0].Image != pushedCoordinate {
			t.Errorf("%s recorded the container running %q, want %q: a teardown that cannot say what a container ran cannot sweep the image it ran", entry.Name, entry.Containers[0].Image, pushedCoordinate)
		}
	}
	if stacks == 0 {
		t.Fatalf("the deploy wrote no app stack at all among %d entries, so the container it started was recorded nowhere", len(entries))
	}
}

func TestTheLedgerRecordAContainerDeployStagesIsTheOneItsPromotionLooksUp(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/"))); !result.GetSuccess() {
		t.Fatalf("Deploy() of a container app = %q", result.GetError())
	}

	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	active, promoted, err := releases.ReadActive(context.Background(), "")
	if err != nil || !promoted {
		t.Fatalf("ReadActive() = %v, %v, want the promotion the deploy made", promoted, err)
	}
	build := active.Builds["web"]
	record, found, err := releases.Record(context.Background(), "web", build)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("the deployments ledger has no record under %q, which is what the promotion names for the container app: every rollback and every re-point would refuse by name", build)
	}
	if record.Image != pushedCoordinate {
		t.Errorf("the record under %q names image %q, want %q, the image a promotion reading it puts in front of the app", build, record.Image, pushedCoordinate)
	}
	if record.Origin == "" || record.Origin != "https://"+record.Physical+".ctr.fake.invalid" {
		t.Errorf("the record under %q names origin %q for container %q, want the URL the provider serves the container on: an edge that fronts a container by URL has nothing else to reach", build, record.Origin, record.Physical)
	}
}
