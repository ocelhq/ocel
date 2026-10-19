package providerserver_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func infraProvisions(stacks *fake.Stacks) int {
	provisioned := 0
	for _, spec := range stacks.Provisioned() {
		if spec.Kind == provider.StackInfra {
			provisioned++
		}
	}
	return provisioned
}

func infraPlans(stacks *fake.Stacks) int {
	planned := 0
	for _, spec := range stacks.Planned() {
		if spec.Kind == provider.StackInfra {
			planned++
		}
	}
	return planned
}

func declaringNoResources(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	req.Manifest.Resources, req.Manifest.Usages = nil, nil
	return req
}

func bucketResource(name string) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: name},
	}
}

func deploySucceeded(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) {
	t.Helper()
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
}

func readInfraRecord(t *testing.T, vendor *fake.Provider) (stackrecords.Stack, bool) {
	t.Helper()
	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name.IsInfra() {
			return entry.Stack, true
		}
	}
	return stackrecords.Stack{}, false
}

func servedConfigured(t *testing.T, version string, p provider.Provider, config *contractv1.ProviderConfig) contractv1connect.ProviderServiceClient {
	t.Helper()
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: version,
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	config.ProjectDir = workingDir(t)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: config}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, client)
	return client
}

func TestADeployDeclaringNoResourcesProvisionsNoInfraStackAndRecordsNone(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 0 {
		t.Errorf("a deploy declaring no resources provisioned the infra stack %d times, want none: an empty stack over nothing changes nothing", provisioned)
	}
	if record, recorded := readInfraRecord(t, vendor); recorded {
		t.Errorf("a deploy declaring no resources recorded the infra stack %+v, want no record of a stack it never provisioned", record)
	}
}

func TestADeployAddingAResourceAfterOneDeclaringNoneTellsTheStacksPortNoRecordNamesTheInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	deploySucceeded(t, client, deployRequest())

	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.Kind == provider.StackInfra && spec.Recorded {
			t.Error("the deploy adding a resource says its records name the infra stack, so a vendor would deploy over a stack of that name it finds but never made")
		}
	}
}

func TestADeployDeclaringNoResourcesOverInfraItEmptiedProvisionsNoInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())
	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("the infra was provisioned %d times over three deploys, want twice: the third declares nothing over a stack the second emptied", provisioned)
	}
}

func TestASecondDeployOfUnchangedInfraProvisionsNoInfraStackAndGrantsWhatItPublished(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())

	deploySucceeded(t, client, deployRequest())

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 1 {
		t.Errorf("two deploys of the same infra provisioned it %d times, want once", provisioned)
	}
	specs := vendor.FakeStacks().Provisioned()
	grants := specs[len(specs)-1].App.Grants
	if len(grants) != 1 || grants[0].Name != "orders" || grants[0].Properties[provider.PropertyPassword] != "fake-"+provider.PropertyPassword {
		t.Errorf("the second deploy grants its app %v, want orders whole, with the password the first deploy published", grants)
	}
}

func TestADeployOverInfraWhoseResourcesChangedProvisionsIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())

	changed := deployRequest()
	changed.Manifest.Resources = append(changed.Manifest.Resources, bucketResource("uploads"))
	deploySucceeded(t, client, changed)

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("a deploy declaring another resource provisioned the infra %d times over two deploys, want twice", provisioned)
	}
}

func TestADeployByAnotherProviderVersionProvisionsUnchangedInfra(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	deploySucceeded(t, servedConfigured(t, "1.0.0", vendor, &contractv1.ProviderConfig{}), deployRequest())

	deploySucceeded(t, servedConfigured(t, "1.1.0", vendor, &contractv1.ProviderConfig{}), deployRequest())

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("a deploy by another provider version provisioned the infra %d times over two deploys, want twice: the program it runs may differ", provisioned)
	}
}

func TestADeployWithOtherProviderOptionsProvisionsUnchangedInfra(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	deploySucceeded(t, servedConfigured(t, "1.0.0", vendor, &contractv1.ProviderConfig{}), deployRequest())

	options, err := structpb.NewStruct(map[string]any{"region": "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	deploySucceeded(t, servedConfigured(t, "1.0.0", vendor, &contractv1.ProviderConfig{Options: options}), deployRequest())

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("a deploy configured with other provider options provisioned the infra %d times over two deploys, want twice", provisioned)
	}
}

func TestADeployListingTransformsProvisionsUnchangedInfra(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{})
	client := servedListingTransforms(t, renderingTransforms{Provider: vendor})
	deploySucceeded(t, client, deployRequest())

	deploySucceeded(t, client, deployRequest())

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("a deploy listing transforms provisioned unchanged infra %d times over two deploys, want twice: a transform reads what no record holds", provisioned)
	}
}

func TestADeployOverInfraWhoseLastProvisioningFailedProvisionsIt(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	stacks := &failingInfraStacks{Stacks: base.Stacks()}
	client := servedBy(t, refusingStacks{Provider: base, stacks: stacks})
	deploySucceeded(t, client, deployRequest())

	stacks.failing.Store(true)
	changed := deployRequest()
	changed.Manifest.Resources = append(changed.Manifest.Resources, bucketResource("uploads"))
	if result, _ := deploy(t, client, changed); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the failed provisioning reported")
	}

	stacks.failing.Store(false)
	deploySucceeded(t, client, deployRequest())

	if provisioned := infraProvisions(base.FakeStacks()); provisioned != 3 {
		t.Errorf("the infra was provisioned %d times over three deploys, want three: the deploy after a failed provisioning cannot know what the stack holds", provisioned)
	}
}

func TestADeployDeclaringNoResourcesOverInfraHoldingSomeProvisionsItEmpty(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())

	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Fatalf("the infra was provisioned %d times over two deploys, want twice: the deploy declaring nothing removes what the first provisioned", provisioned)
	}
	if held := lastInfraResources(vendor); len(held) != 0 {
		t.Errorf("the deploy declaring no resources left the infra stack holding %v, want nothing", held)
	}
}

func TestADeployDeclaringNoResourcesAfterAFailedRemovalProvisionsTheInfraAgain(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	stacks := &failingInfraStacks{Stacks: base.Stacks()}
	client := servedBy(t, refusingStacks{Provider: base, stacks: stacks})
	deploySucceeded(t, client, deployRequest())

	stacks.failing.Store(true)
	if result, _ := deploy(t, client, declaringNoResources(deployRequest())); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the failed provisioning reported")
	}

	stacks.failing.Store(false)
	deploySucceeded(t, client, declaringNoResources(deployRequest()))

	if provisioned := infraProvisions(base.FakeStacks()); provisioned != 3 {
		t.Errorf("the infra was provisioned %d times over three deploys, want three: a removal that failed may have left what it was removing", provisioned)
	}
}

func TestADryDeployOverUnchangedInfraPlansNoInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())

	dry := deployRequest()
	dry.Dry = true
	result, events := deploy(t, client, dry)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if planned := infraPlans(vendor.FakeStacks()); planned != 0 {
		t.Errorf("a dry deploy over unchanged infra planned the infra stack %d times, want none", planned)
	}
	if group := groupOfKind(lastPlan(events), provider.StackGroupKind); group != nil && strings.HasSuffix(group.GetName(), "--infra") {
		t.Errorf("the plan shows the infra stack as %v, want nothing to change", group)
	}
}

func TestADryDeployDeclaringNoResourcesPlansNoInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	dry := declaringNoResources(deployRequest())
	dry.Dry = true
	if result, _ := deploy(t, client, dry); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if planned := infraPlans(vendor.FakeStacks()); planned != 0 {
		t.Errorf("a dry deploy declaring no resources planned the infra stack %d times, want none", planned)
	}
	if _, recorded := readInfraRecord(t, vendor); recorded {
		t.Error("a dry deploy recorded the infra stack, want nothing written")
	}
}

func TestProvisionInfraOfUnchangedInfraProvisionsNoInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	provisionedInfra(t, client, infraRequest(deployRequest()))

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 1 {
		t.Errorf("two ProvisionInfra calls over the same infra provisioned it %d times, want once", provisioned)
	}
}

func TestRemoveProjectAfterADeployDeclaringNoResourcesForgetsTheInfraStack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, declaringNoResources(deployRequest()))
	if _, recorded := readInfraRecord(t, vendor); recorded {
		t.Fatal("the deploy declaring no resources recorded an infra stack, want none for removal to find")
	}

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed though it records no infra stack", result.GetError())
	}
	if record, recorded := readInfraRecord(t, vendor); recorded {
		t.Errorf("the removed project still records its infra stack %+v", record)
	}
}

func TestADeployDeclaringNoResourcesOverInfraHoldingADatabaseIsRefusedWhenItsConsentedPlanShowsNoRemoval(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())

	req := consentedPlan(t, client, declaringNoResources(deployRequest()))
	req.Consented.Groups = slices.DeleteFunc(req.Consented.GetGroups(), func(group *planv1.ChangeGroup) bool {
		return strings.HasSuffix(group.GetName(), "--infra")
	})
	_, _, err := deployStream(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("Deploy() = %v, want it refused for removing orders, which the consented plan never showed", err)
	}
	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 1 {
		t.Errorf("the refused deploy provisioned the infra %d times in all, want only the first deploy's", provisioned)
	}
}

func unpublishOrders(t *testing.T, vendor *fake.Provider) {
	t.Helper()
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	if removed, err := store.RemoveBinding(context.Background(), scope, storedBindings(t, vendor)["orders"].Environment, "orders"); err != nil || !removed {
		t.Fatalf("RemoveBinding(orders) = %v, %v", removed, err)
	}
}

func TestADeployOverUnchangedInfraWhoseBindingIsNoLongerPublishedProvisionsItAgain(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	deploySucceeded(t, client, deployRequest())
	unpublishOrders(t, vendor)

	deploySucceeded(t, client, deployRequest())

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("the infra was provisioned %d times over two deploys, want twice: only provisioning publishes the secrets the record never keeps", provisioned)
	}
	if _, published := storedBindings(t, vendor)["orders"]; !published {
		t.Error("the second deploy left orders unpublished")
	}
}

func TestProvisionInfraOverUnchangedInfraWhoseBindingIsNoLongerPublishedProvisionsItAgain(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	unpublishOrders(t, vendor)

	provisionedInfra(t, client, infraRequest(deployRequest()))

	if provisioned := infraProvisions(vendor.FakeStacks()); provisioned != 2 {
		t.Errorf("the infra was provisioned %d times over two ProvisionInfra calls, want twice: the deploy after it is refused while orders is unpublished", provisioned)
	}
}
