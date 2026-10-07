package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

func infraRefusal(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ProvisionInfraRequest) string {
	t.Helper()
	result, err := provisionInfraStream(t, client, req)
	if err == nil && result.GetSuccess() {
		t.Fatal("ProvisionInfra() succeeded, want it refused")
	}
	return result.GetError() + connectMessage(err)
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
	if result.GetSuccess() || !strings.Contains(result.GetError(), "was never provisioned") {
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

func TestProvisionInfraProvisionsTheTopicsOfWorkersWhoseAppsAreNotBuiltYet(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)).WithFacts(runningWorkers)
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: string(provider.ComputeServerless)}}
	provisionedInfra(t, client, infraRequest(req))

	specs := vendor.FakeStacks().Provisioned()
	if len(specs) != 1 || len(specs[0].Resources) != 2 {
		t.Fatalf("ProvisionInfra() provisioned %d stacks, want the infra stack with orders and resize-image", len(specs))
	}
}

func TestProvisionInfraRefusesAConsumerOnAWorkerTheManifestDoesNotDeclare(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)).WithFacts(runningWorkers)
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	_, err := provisionInfraStream(t, client, infraRequest(req))
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("ProvisionInfra() = %v, want it refused as invalid: the task's consumer runs on no declared worker", err)
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none", len(specs))
	}
}

func legacyResource() *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "legacy",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "legacy"},
	}
}

func lastInfraResources(vendor *fake.Provider) []string {
	var names []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.Kind != provider.StackInfra {
			continue
		}
		names = names[:0]
		for _, resource := range spec.Resources {
			names = append(names, resource.Name)
		}
	}
	slices.Sort(names)
	return names
}

func TestProvisionInfraKeepsAResourceNoLongerDeclaredUntilTheDeployOverItRemovesIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	before := deployRequest()
	before.Manifest.Resources = append(before.Manifest.Resources, legacyResource())
	provisionedInfra(t, client, infraRequest(before))
	before.InfraProvisioned = true
	if result, _ := deploy(t, client, before); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	after := deployRequest()
	provisionedInfra(t, client, infraRequest(after))

	if held := lastInfraResources(vendor); !slices.Equal(held, []string{"legacy", "orders"}) {
		t.Errorf("ProvisionInfra() left the infra stack holding %v, want legacy kept beside orders: a build that fails next leaves the live release reading legacy", held)
	}
	if _, published := storedBindings(t, vendor)["legacy"]; !published {
		t.Error("ProvisionInfra() pruned legacy's binding, which the live release still reads until a deploy replaces it")
	}

	after.InfraProvisioned = true
	if result, _ := deploy(t, client, after); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if held := lastInfraResources(vendor); !slices.Equal(held, []string{"orders"}) {
		t.Errorf("the deploy over the infra left it holding %v, want only orders: the deploy that no longer declares legacy removes it", held)
	}
	if _, published := storedBindings(t, vendor)["legacy"]; published {
		t.Error("the deploy that removed legacy left its binding published")
	}
}

func TestADeployOverInfraHoldingOnlyWhatItDeclaresProvisionsNoInfra(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))

	req.InfraProvisioned = true
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	infra := 0
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.Kind == provider.StackInfra {
			infra++
		}
	}
	if infra != 1 {
		t.Errorf("the infra stack was provisioned %d times, want once, by ProvisionInfra", infra)
	}
}

func TestProvisionInfraRefusesAProductionProjectThatDeclaresNoHostname(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := infraRequest(deployRequest())
	req.Manifest.Domains = nil
	if said := infraRefusal(t, client, req); !strings.Contains(said, "domains.production") {
		t.Fatalf("ProvisionInfra() = %q, want it refused for the production domain the project does not declare", said)
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none: the deploy it precedes is refused", len(specs))
	}
}

func TestProvisionInfraRefusesATaskOnAProviderThatRunsNoWorker(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: string(provider.ComputeServerless)}}
	if said := infraRefusal(t, client, infraRequest(req)); !strings.Contains(said, "unsupported") {
		t.Fatalf("ProvisionInfra() = %q, want it refused as unsupported", said)
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused ProvisionInfra provisioned %d stacks, want none", len(specs))
	}
}

type failingInfraStacks struct {
	provider.Stacks
	failing atomic.Bool
}

func (s *failingInfraStacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	if spec.Kind == provider.StackInfra && s.failing.Load() {
		result, err := s.Stacks.Provision(ctx, spec, progress)
		if err != nil {
			return result, err
		}
		return provider.StackResult{}, errors.New("the stack changed, then its outputs could not be read")
	}
	return s.Stacks.Provision(ctx, spec, progress)
}

func TestADeployOverInfraWhoseLastProvisioningFailedIsRefused(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	stacks := &failingInfraStacks{Stacks: base.Stacks()}
	client := servedBy(t, refusingStacks{Provider: base, stacks: stacks})
	earlier := deployRequest()
	provisionedInfra(t, client, infraRequest(earlier))

	stacks.failing.Store(true)
	later := deployRequest()
	later.Manifest.Resources = append(later.Manifest.Resources, legacyResource())
	if result, err := provisionInfraStream(t, client, infraRequest(later)); err == nil && result.GetSuccess() {
		t.Fatal("ProvisionInfra() succeeded, want the failed provisioning reported")
	}

	earlier.InfraProvisioned = true
	result, _ := deploy(t, client, earlier)
	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded over infra a failed provisioning changed after the earlier one, want it refused: the stack no longer holds what the apps were built against")
	}
}

func readProjectRecord(t *testing.T, vendor *fake.Provider) (stackrecords.Project, bool) {
	t.Helper()
	row, err := keyvalue.ReadOrEmpty(context.Background(), vendor.KeyValues(), stackrecords.ProjectKey(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Value) == 0 {
		return stackrecords.Project{}, false
	}
	var recorded stackrecords.Project
	if err := json.Unmarshal(row.Value, &recorded); err != nil {
		t.Fatal(err)
	}
	return recorded, true
}

func TestProvisionInfraRecordsAProjectNoDeployRecordedYet(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	provisionedInfra(t, client, infraRequest(deployRequest()))

	if _, recorded := readProjectRecord(t, vendor); !recorded {
		t.Error("ProvisionInfra() recorded no project, so if the build then fails nothing finds the infra it left to tear down")
	}
}

func TestProvisionInfraKeepsTheFeaturesADeployRecordedForTheProject(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	recordProject(t, vendor, "shop", fake.FeatureCache, fake.FeatureImages)

	provisionedInfra(t, client, infraRequest(deployRequest()))

	recorded, _ := readProjectRecord(t, vendor)
	if !slices.Equal(recorded.Features, []string{fake.FeatureCache, fake.FeatureImages}) {
		t.Errorf("after ProvisionInfra() the project records features %v, want the ones its deploy recorded: it reads no app framework", recorded.Features)
	}
}

func TestProvisionInfraRecordsAPersistentPreviewWithTheAliasItsBuildWasGiven(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	req := infraRequest(previewRequest())
	req.AliasToken = "abcdefghijklmnop"
	provisionedInfra(t, client, req)

	meta, err := stackrecords.ReadEnvironmentMeta(context.Background(), vendor.KeyValues(), environment.TierPreview, "shop", "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Lifecycle != stackrecords.LifecyclePersistent || meta.AliasToken != "abcdefghijklmnop" {
		t.Errorf("after ProvisionInfra() pr-7 records lifecycle %q and alias token %q, want persistent and abcdefghijklmnop: a preview whose build fails still has infra to reclaim",
			meta.Lifecycle, meta.AliasToken)
	}
}
