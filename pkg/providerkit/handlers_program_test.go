package providerkit_test

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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

func TestPreviewDeployStampsTheWildcardItIsServedOn(t *testing.T) {
	builtProject(t)
	client, provider := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, provider, providerkit.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	state := readStack(t, provider, providerkit.ClassPreview, "shop")
	if !state.Edge.ServedOnGlobalPreview("preview.acme.com") {
		t.Errorf("the stack records %q, want the wildcard every preview of it is served on", state.Edge.GlobalPreview)
	}
	stacks := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if len(stacks) == 0 || stacks[0].Program.Worker.Vars[fake.ProgramPreviewVar] != "preview.acme.com" {
		t.Errorf("the program carries %+v, want the wildcard's base domain", stacks)
	}
}

func TestPreviewDeployWithItsOwnWildcardIsNotStampedOnTheSharedOne(t *testing.T) {
	builtProject(t)
	client, provider := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, provider, providerkit.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})

	req := previewDeployRequest()
	req.Manifest.Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Hostnames: []string{"*.preview.shop.example"},
	}}
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	state := readStack(t, provider, providerkit.ClassPreview, "shop")
	if state.Edge.GlobalPreview != "" {
		t.Errorf("the stack records %q, want nothing: this project serves its previews on a wildcard of its own", state.Edge.GlobalPreview)
	}
	stacks := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if len(stacks) == 0 || stacks[0].Program.Worker.Vars[fake.ProgramPreviewVar] != "" {
		t.Errorf("the program carries %+v, want no shared base domain", stacks)
	}
}
