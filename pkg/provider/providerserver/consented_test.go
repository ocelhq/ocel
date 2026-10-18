package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

func TestProvisionInfraRefusesAnInfraStackItsConsentedPlanNeverShowedBeforeWritingAnything(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := infraRequest(deployRequest())
	req.Consented = &planv1.ChangePlan{}
	if refused := infraRefusal(t, client, req); !strings.Contains(refused, "orders") || !strings.Contains(refused, "nobody consented to") {
		t.Fatalf("ProvisionInfra() = %q, want a refusal naming the database nobody consented to", refused)
	}
	if provisioned := vendor.FakeStacks().Provisioned(); len(provisioned) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none", len(provisioned))
	}
	if entry, err := vendor.KeyValues().Read(context.Background(), stackrecords.ProjectKey(environment.TierProduction, "shop")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("the refused ProvisionInfra recorded the project %s (%v), want the refusal before any write", entry.Value, err)
	}
}

func withoutConsentedGroups(req *contractv1.DeployRequest, kind string) *contractv1.DeployRequest {
	req.Consented.Groups = slices.DeleteFunc(req.Consented.Groups, func(group *planv1.ChangeGroup) bool { return group.GetKind() == kind })
	return req
}

func TestADeployWhoseConsentedPlanLacksTheEdgeIsRefusedBeforeItRecordsAGateOrReconcilesTheEdge(t *testing.T) {
	client, vendor := productionServed(t)

	req := withoutConsentedGroups(consentedPlan(t, client, protecting(productionDeployRequest())), edge.EdgeGroupKind)
	_, _, err := deployStream(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "nobody consented to") {
		t.Fatalf("Deploy() = %v, want it refused: the edge it reconciles was never on the plan the human consented to", err)
	}
	if _, held := recordedGateIn(t, vendor, environment.TierProduction, "shop"); held {
		t.Error("the refused deploy recorded a gate")
	}
	if reconciled := reconciledEdgeStacks(vendor); reconciled != 0 {
		t.Errorf("the refused deploy reconciled the edge %d times, want none", reconciled)
	}
}

func TestAPreviewDeployWhoseConsentedPlanLacksTheEdgeIsRefusedBeforeItAssignsAnAlias(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	req := withoutConsentedGroups(consentedPlan(t, client, previewRequest()), edge.EdgeGroupKind)
	if _, _, err := deployStream(t, client, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() = %v, want it refused: the edge it reconciles was never on the plan the human consented to", err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(context.Background(), vendor.KeyValues(), environment.TierPreview, "shop", "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if meta.AliasToken != "" {
		t.Errorf("the refused deploy assigned the preview alias %s, want none", meta.AliasToken)
	}
}

func TestADeployWhoseConsentedPlanLacksAPartItsPreflightAddsIsRefusedBeforeThePreflightApplies(t *testing.T) {
	builtProject(t)
	vendor := &addingParts{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := withoutConsentedGroups(consentedPlan(t, client, twoAppRequest()), provider.StackGroupKind)
	if _, _, err := deployStream(t, client, req); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "fake/tasks") {
		t.Fatalf("Deploy() = %v, want it refused naming the part nobody consented to", err)
	}
	if vendor.applied != 0 {
		t.Errorf("the refused deploy applied its preflight %d times, want none", vendor.applied)
	}
}
