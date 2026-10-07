package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func infraRequest(req *contractv1.DeployRequest) *contractv1.ProvisionInfraRequest {
	manifest := req.GetManifest()
	return &contractv1.ProvisionInfraRequest{
		Manifest: &contractv1.Manifest{
			Slug:      manifest.GetSlug(),
			Resources: manifest.GetResources(),
			Domains:   manifest.GetDomains(),
			Workers:   manifest.GetWorkers(),
		},
		Environment:    req.GetEnvironment(),
		Edge:           req.GetEdge(),
		InlineBindings: req.GetInlineBindings(),
	}
}

func provisionInfraStream(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ProvisionInfraRequest) (*progressv1.OperationResult, error) {
	t.Helper()
	stream, err := client.ProvisionInfra(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionInfra() error = %v", err)
	}
	defer stream.Close()
	var result *progressv1.OperationResult
	for stream.Receive() {
		if got := stream.Msg().GetResult(); got != nil {
			result = got
		}
	}
	return result, stream.Err()
}

func provisionedInfra(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ProvisionInfraRequest) {
	t.Helper()
	result, err := provisionInfraStream(t, client, req)
	if err != nil {
		t.Fatalf("ProvisionInfra() stream error = %v", err)
	}
	if !result.GetSuccess() {
		t.Fatalf("ProvisionInfra() = %q, want it to succeed", result.GetError())
	}
}

func reconciledEdgeStacks(vendor *fake.Provider) int {
	edges := vendor.Edges().(*fake.Edges)
	return len(edges.Edge(fake.KindDirect).Stacks()) + len(edges.Edge(fake.KindRelay).Stacks())
}

func TestProvisionInfraProvisionsTheInfraStackAloneAndPublishesItsBindings(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	provisionedInfra(t, client, infraRequest(deployRequest()))

	specs := vendor.FakeStacks().Provisioned()
	if len(specs) != 1 || specs[0].Kind != provider.StackInfra {
		t.Fatalf("ProvisionInfra() provisioned %d stacks, want the infra stack alone", len(specs))
	}
	if published := storedBindings(t, vendor); published["orders"].Owner != variablestore.OwnerOcel {
		t.Errorf("ProvisionInfra() published %v, want orders published for the build and the apps to read", published)
	}
	if reconciled := reconciledEdgeStacks(vendor); reconciled != 0 {
		t.Errorf("ProvisionInfra() reconciled the edge %d times, want none: with no app deployed it would prune every route", reconciled)
	}
}

func TestADeployAfterProvisionInfraProvisionsOnlyItsAppsAndGrantsWhatInfraPublished(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))

	req.InfraProvisioned = true
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed over the infra ProvisionInfra provisioned", result.GetError())
	}
	specs := vendor.FakeStacks().Provisioned()
	if len(specs) != 2 || specs[1].Kind != provider.StackApp {
		t.Fatalf("the stacks port saw %d specs, want the infra stack once and then the app stack", len(specs))
	}
	if !slices.ContainsFunc(specs[1].App.Grants, func(binding provider.Binding) bool { return binding.Name == "orders" }) {
		t.Errorf("the app spec grants %v, want orders, which ProvisionInfra published", specs[1].App.Grants)
	}
}

func TestADeployRefusesInfraProvisionedFromOtherResources(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))

	req.InfraProvisioned = true
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "uploads",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads"},
	})
	result, _ := deploy(t, client, req)
	if result.GetSuccess() || !strings.Contains(result.GetError(), "another deploy provisioned it") {
		t.Fatalf("Deploy() = %q, want it refused: the infra stack holds other resources than the apps were built against", result.GetError())
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 1 {
		t.Errorf("the refused deploy left %d stacks provisioned, want only the infra stack: no app ships against infra it did not declare", len(specs))
	}
}

func TestADeployRefusesInfraProvisionedWhenNoneWas(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.InfraProvisioned = true
	result, _ := deploy(t, client, req)
	if result.GetSuccess() || !strings.Contains(result.GetError(), "another deploy provisioned it") {
		t.Fatalf("Deploy() = %q, want it refused: no infra stack was provisioned for it", result.GetError())
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want none", len(specs))
	}
}

func TestADryDeployRefusesInfraProvisioned(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Dry, req.InfraProvisioned = true, true
	_, _, err := deployStream(t, client, req)
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("Deploy() = %v, want it refused as invalid: a dry deploy plans its infra with its apps", err)
	}
}

func TestProvisionInfraRefusesAnEphemeralPreview(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := infraRequest(deployRequest())
	req.Environment = &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Identity:  "pr-1",
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
	}
	_, err := provisionInfraStream(t, client, req)
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("ProvisionInfra() = %v, want it refused as invalid: an ephemeral preview has no infra stack", err)
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none", len(specs))
	}
}

func TestProvisionInfraRefusesAManifestDeclaringApps(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := infraRequest(deployRequest())
	req.Manifest.Apps = deployRequest().GetManifest().GetApps()
	_, err := provisionInfraStream(t, client, req)
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("ProvisionInfra() = %v, want it refused as invalid: apps ship through Deploy", err)
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none", len(specs))
	}
}
