package providerserver_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func consentedPlan(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) *contractv1.DeployRequest {
	t.Helper()
	dry := proto.CloneOf(req)
	dry.Dry = true
	result, events := deploy(t, client, dry)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("the dry deploy = %q, want the plan drawn", result.GetError())
	}
	plan := lastPlan(events)
	if plan == nil {
		t.Fatal("the dry deploy streamed no plan to consent to")
	}
	applied := proto.CloneOf(req)
	applied.Consented = plan
	return applied
}

func TestADeployThatSendsTheDryRunsPlanAppliesIt(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, consentedPlan(t, client, deployRequest()))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with the plan its dry run drew = %q, want it applied", result.GetError())
	}
	if provisioned := p.FakeStacks().Provisioned(); len(provisioned) != 2 {
		t.Errorf("the deploy provisioned %d stacks, want the infra stack and the app stack the plan showed", len(provisioned))
	}
}

func TestADeployRefusesWorkItsConsentedPlanNeverShowed(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	req := consentedPlan(t, client, deployRequest())
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "billing",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "billing"},
	})
	_, _, err := deployStream(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() = %v, want it refused as invalid: the database it would create was never on the plan the human consented to", err)
	}
	if !strings.Contains(err.Error(), "billing") || !strings.Contains(err.Error(), "nobody consented to") {
		t.Errorf("Deploy() = %v, want a refusal naming the database nobody consented to", err)
	}
	if provisioned := p.FakeStacks().Provisioned(); len(provisioned) != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want the refusal before anything is provisioned", len(provisioned))
	}
}

func TestADryDeployThatSendsAConsentedPlanIsRefused(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := consentedPlan(t, client, deployRequest())
	req.Dry = true
	if _, _, err := deployStream(t, client, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() = %v, want a dry deploy carrying a consented plan refused: it applies nothing for the plan to bound", err)
	}
}
