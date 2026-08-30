package providerkit_test

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const containerTestImage = "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerDeployRequest(probe string) *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Apps[0].Compute = string(providerkit.ComputeContainer)
	req.Manifest.Functions = nil
	req.Manifest.Containers = []*contractv1.ManifestContainer{{
		App:             req.Manifest.Apps[0].GetName(),
		Image:           containerTestImage,
		HealthCheckPath: probe,
	}}
	return req
}

func TestTheWireAcceptsAnAppNamingContainer(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := containerDeployRequest("/")

	result, _, err := deployStream(t, client, req)
	if err != nil {
		t.Fatalf("Deploy() with an app naming %q: error = %v, want the wire pin to admit it — it is the only compute the VPS provider runs", providerkit.ComputeContainer, err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with an app naming %q = %q, want it to succeed", providerkit.ComputeContainer, result.GetError())
	}
}

func TestTheWireRefusesAnAppThatNamesNoCompute(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Compute = ""

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app naming no compute: code = %v, want %v — the manifest pin is what makes compute non-empty by the time a provider reads it", got, connect.CodeInvalidArgument)
	}
}

func TestTheWireRefusesAnAppNamingAComputeOutsideTheVocabulary(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Compute = "vm"

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app naming %q: code = %v, want %v", "vm", got, connect.CodeInvalidArgument)
	}
}

func TestTheAppPlanCarriesTheImageAndProbeAContainerAppIsStoodUpFrom(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	result, _ := deploy(t, client, containerDeployRequest("/healthz"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app == nil {
		t.Fatal("the last plan the releaser saw stands up no app")
	}
	if app.Compute != providerkit.ComputeContainer {
		t.Errorf("Compute = %q, want %q: the primitive is chosen by what the plan names", app.Compute, providerkit.ComputeContainer)
	}
	if app.Image != containerTestImage {
		t.Errorf("Image = %q, want the pinned image the manifest carries", app.Image)
	}
	if app.HealthCheckPath != "/healthz" {
		t.Errorf("HealthCheckPath = %q, want the path the manifest says the process is probed at", app.HealthCheckPath)
	}
}

func TestAServerlessAppPlanNamesItsComputeAndCarriesNoImage(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app.Compute != providerkit.ComputeServerless {
		t.Errorf("Compute = %q, want %q", app.Compute, providerkit.ComputeServerless)
	}
	if app.Image != "" || app.HealthCheckPath != "" {
		t.Errorf("Image = %q and HealthCheckPath = %q, want a serverless app to carry neither", app.Image, app.HealthCheckPath)
	}
}

func TestTheContainerAStoodUpAppRunsOnIsRecordedAgainstItsStack(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	entries, err := providerkit.ReadStacks(context.Background(), provider.Records(), providerkit.ClassProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Kind != providerkit.StackApp {
			continue
		}
		if len(entry.Containers) != 1 || entry.Containers[0].Name != "web" {
			t.Fatalf("%s recorded %v, want the container it stood the app up as: nothing can take down what was never written", entry.Name, entry.Containers)
		}
	}
}
