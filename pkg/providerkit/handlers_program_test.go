package providerkit_test

import (
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type unprogrammed struct{ providerkit.Provider }

func edged(kind edge.Kind, zone string) *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{
		Kind: string(kind),
		Dns:  &contractv1.Dns{Kind: string(fake.KindZone), Zone: zone},
	}
}

func previewDeployRequest() *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Domains = nil
	req.Environment = &environmentv1.Environment{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Identity: "pr-7",
	}
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	return req
}

func previewBootstrapped(t *testing.T, client contractv1connect.ProviderServiceClient) {
	t.Helper()
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})
}

func TestUsePreviewWildcardCarriesTheProviderProgramToAnEdgeThatRunsCode(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")

	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	specs := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Specs()
	if len(specs) != 1 {
		t.Fatalf("the edge reconciled %d wildcards, want the one raised", len(specs))
	}
	spec := specs[0]
	if spec.Program == nil {
		t.Fatal("the wildcard carries no program, and the relay edge answers every preview from an entry worker")
	}
	if spec.Program.StoreScriptName != fake.ProgramStore {
		t.Errorf("StoreScriptName = %q, want %q", spec.Program.StoreScriptName, fake.ProgramStore)
	}
	if spec.Program.Worker.Vars[fake.ProgramPreviewVar] != "preview.acme.com" {
		t.Errorf("Vars[%s] = %q, want the wildcard's base domain",
			fake.ProgramPreviewVar, spec.Program.Worker.Vars[fake.ProgramPreviewVar])
	}
	if spec.Values[fake.ProgramEdgeVar] != string(fake.KindRelay) {
		t.Errorf("Values = %v, want the values the provider hands the entry", spec.Values)
	}
}

func TestUsePreviewWildcardLeavesAnEdgeThatRunsNoCodeUnprogrammed(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")

	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindDirect, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	specs := provider.Edges().(*fake.Edges).Edge(fake.KindDirect).Specs()
	if len(specs) != 1 {
		t.Fatalf("the edge reconciled %d wildcards, want the one raised", len(specs))
	}
	if specs[0].Program != nil {
		t.Errorf("the wildcard carries %+v, want no program: the direct edge runs none of our code", specs[0].Program)
	}
}

func TestUsePreviewWildcardRefusesAnEdgeThatRunsCodeForAProviderThatWritesNoProgram(t *testing.T) {
	t.Parallel()
	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.0.0", unprogrammed{provider})

	stream, err := client.UsePreviewWildcard(t.Context(), &contractv1.UsePreviewWildcardRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		BaseDomain: "preview.acme.com",
		Edge:       edged(fake.KindRelay, "acme.com"),
	})
	if err != nil {
		t.Fatalf("UsePreviewWildcard() error = %v", err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if !strings.Contains(said, string(fake.KindRelay)) || !strings.Contains(said, "entry worker") {
		t.Fatalf("UsePreviewWildcard() = %q, want it refused by the edge's name and the worker it runs", said)
	}
}

func TestDeployCarriesTheProviderProgramToAnEdgeThatRunsCode(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	stacks := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if len(stacks) != 1 {
		t.Fatalf("the edge reconciled %d stacks, want the one this deploy stands up", len(stacks))
	}
	spec := stacks[0]
	if spec.Program == nil {
		t.Fatal("the stack carries no program, and the relay edge answers every request from an entry worker")
	}
	if spec.Program.Name != fake.ProgramName("shop", providerkit.ClassProduction) {
		t.Errorf("Name = %q, want %q", spec.Program.Name, fake.ProgramName("shop", providerkit.ClassProduction))
	}
	if spec.Program.Worker.Vars[fake.ProgramPreviewAppsVar] != "web" {
		t.Errorf("Vars[%s] = %q, want the manifest's app names",
			fake.ProgramPreviewAppsVar, spec.Program.Worker.Vars[fake.ProgramPreviewAppsVar])
	}
	if spec.Values[fake.ProgramEdgeVar] != string(fake.KindRelay) {
		t.Errorf("Values = %v, want the values the provider hands the entry", spec.Values)
	}
	if spec.PruneOnly {
		t.Error("the production stack is prune-only, though its worker is the one serving every request")
	}
	if !spec.PruneRoutes {
		t.Error("the stack keeps routes it no longer serves, want the edge sweeping them as it reconciles")
	}
	if !slices.Equal(spec.Domains, []string{"shop.example"}) {
		t.Errorf("Domains = %v, want the hostnames the manifest declares for production", spec.Domains)
	}
}

func TestDeployLeavesAnEdgeThatRunsNoCodeUnprogrammed(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	stacks := provider.Edges().(*fake.Edges).Edge(fake.KindDirect).Stacks()
	if len(stacks) != 1 {
		t.Fatalf("the edge reconciled %d stacks, want the one this deploy stands up", len(stacks))
	}
	if stacks[0].Program != nil {
		t.Errorf("the stack carries %+v, want no program: the direct edge runs none of our code", stacks[0].Program)
	}
}

func TestDeployRefusesAnEdgeThatRunsCodeForAProviderThatWritesNoProgram(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.0.0", unprogrammed{provider})
	standsBootstrapped(t, client)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	result, _ := deploy(t, client, req)
	said := result.GetError()
	if !strings.Contains(said, string(fake.KindRelay)) || !strings.Contains(said, "entry worker") {
		t.Fatalf("Deploy() = %q, want it refused by the edge's name and the worker it runs", said)
	}
}

func previewDeployed(t *testing.T, req *contractv1.DeployRequest) (*fake.Provider, *progressv1.ResultEvent) {
	t.Helper()
	builtProject(t)
	client, provider := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, provider, providerkit.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	result, _ := deploy(t, client, req)
	return provider, result
}

func previewRefused(t *testing.T, req *contractv1.DeployRequest) string {
	t.Helper()
	builtProject(t)
	client, provider := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, provider, providerkit.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})

	stream, err := client.Deploy(t.Context(), req)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()
	var said string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			said = result.GetError()
		}
	}
	return said + connectMessage(stream.Err())
}

func onlyStack(t *testing.T, provider *fake.Provider) edge.StackSpec {
	t.Helper()
	stacks := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if len(stacks) != 1 {
		t.Fatalf("the edge reconciled %d stacks, want the one this deploy stands up", len(stacks))
	}
	if !stacks[0].PruneRoutes {
		t.Error("the stack keeps routes it no longer serves, want the edge sweeping them as it reconciles")
	}
	return stacks[0]
}

func declaresPreview(req *contractv1.DeployRequest, hostnames ...string) *contractv1.DeployRequest {
	req.Manifest.Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Hostnames: hostnames,
	}}
	return req
}

func TestPreviewDeployOnTheSharedWildcardPrunesItsOwnWorker(t *testing.T) {
	provider, result := previewDeployed(t, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	state := readStack(t, provider, providerkit.ClassPreview, "shop")
	if !state.Edge.ServedOnGlobalPreview("preview.acme.com") {
		t.Errorf("the stack records %q, want the wildcard every preview of it is served on", state.Edge.GlobalPreview)
	}
	spec := onlyStack(t, provider)
	if !spec.PruneOnly {
		t.Error("the stack uploads a worker of its own, though the shared preview entry is what answers on the wildcard")
	}
	if len(spec.Domains) != 0 {
		t.Errorf("Domains = %v, want none: the shared entry holds the route", spec.Domains)
	}
	if got := spec.Program.Worker.Vars[fake.ProgramPreviewVar]; got != "" {
		t.Errorf("Vars[%s] = %q, want empty: the shared entry carries the base domain, not the project's worker",
			fake.ProgramPreviewVar, got)
	}
}

func TestPreviewDeployOnItsOwnWildcardServesFromItsOwnWorker(t *testing.T) {
	provider, result := previewDeployed(t, declaresPreview(previewDeployRequest(), "*.preview.shop.example"))
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	state := readStack(t, provider, providerkit.ClassPreview, "shop")
	if state.Edge.GlobalPreview != "" {
		t.Errorf("the stack records %q, want nothing: this project serves its previews on a wildcard of its own", state.Edge.GlobalPreview)
	}
	spec := onlyStack(t, provider)
	if spec.PruneOnly {
		t.Error("the stack is prune-only, though its own worker is what answers on the project's wildcard")
	}
	if !slices.Equal(spec.Domains, []string{"*.preview.shop.example"}) {
		t.Errorf("Domains = %v, want the project's preview wildcard", spec.Domains)
	}
	if got := spec.Program.Worker.Vars[fake.ProgramPreviewVar]; got != "preview.shop.example" {
		t.Errorf("Vars[%s] = %q, want the base under the project's own wildcard", fake.ProgramPreviewVar, got)
	}
}

func TestPreviewDeployWithNoAppsPrunesItsOwnWorker(t *testing.T) {
	req := declaresPreview(previewDeployRequest(), "*.preview.shop.example")
	req.Manifest.Apps, req.Manifest.Functions = nil, nil

	provider, result := previewDeployed(t, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	spec := onlyStack(t, provider)
	if !spec.PruneOnly {
		t.Error("the stack uploads a worker, though this project has no app left for it to serve")
	}
	if len(spec.Domains) != 0 {
		t.Errorf("Domains = %v, want none: nothing is left to answer on them", spec.Domains)
	}
}

func TestPreviewDeployRefusesAPreviewDomainThatIsNotAWildcard(t *testing.T) {
	said := previewRefused(t, declaresPreview(previewDeployRequest(), "pr-7.preview.shop.example"))
	if !strings.Contains(said, "*.pr-7.preview.shop.example") {
		t.Fatalf("Deploy() = %q, want it refused by the wildcard it should have declared instead", said)
	}
}

func TestPreviewDeployRefusesTwoPreviewDomains(t *testing.T) {
	said := previewRefused(t, declaresPreview(previewDeployRequest(),
		"*.preview.shop.example", "*.preview.acme.example"))
	if !strings.Contains(said, "*.preview.shop.example") || !strings.Contains(said, "*.preview.acme.example") {
		t.Fatalf("Deploy() = %q, want it refused by both domains the project claims", said)
	}
}
