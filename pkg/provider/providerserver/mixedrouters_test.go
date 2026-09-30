package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

const adminHost = "admin.shop.example"

type pairedByCompute struct{ *fake.Provider }

func (p pairedByCompute) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.Pairings = []provider.Pairing{
		{Edge: fake.KindRelay, Router: fake.RouterRelay, Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: fake.KindRelay, Router: fake.RouterDirect, Computes: []provider.Compute{provider.ComputeContainer}},
		{Edge: fake.KindDirect, Router: fake.RouterDirect, Computes: provider.Computes()},
	}
	return facts
}

func mixedServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	p.Edges().(*fake.Edges).Edge(fake.KindRelay).ProxiesRecords()
	client := servedBy(t, pairedByCompute{p})
	return client, p
}

func withContainerAdmin(req *contractv1.DeployRequest, deploymentID string) *contractv1.DeployRequest {
	req = namingARegistry(req)
	admin := &contractv1.ManifestApp{
		Name:         "admin",
		Framework:    &contractv1.Framework{Name: "next"},
		DeploymentId: deploymentID,
		Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
			Image:           containerTestImage,
			HealthCheckPath: "/",
		}},
	}
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PRODUCTION {
		admin.Domains = []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{adminHost}}}
	}
	req.Manifest.Apps = append(req.Manifest.Apps, admin)
	return req
}

func mixedRequest() *contractv1.DeployRequest {
	return withContainerAdmin(deployRequest(), adminDeploymentID)
}

func TestAProjectMixingServerlessAndContainerAppsPromotesEachThroughTheRouterItsComputePairsWith(t *testing.T) {
	client, p := mixedServed(t)

	first, _ := deploy(t, client, mixedRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() of a project mixing serverless and container apps = %q, want it served through one edge", first.GetError())
	}
	planes := p.Routers().(*fake.Routers)
	relayed := planes.DataPlane(fake.RouterRelay).Builds("shop", environment.TierProduction, router.DefaultPointer)
	direct := planes.DataPlane(fake.RouterDirect).Builds("shop", environment.TierProduction, router.DefaultPointer)
	if relayed["web"] == "" || relayed["admin"] != "" {
		t.Errorf("the %s router serves %v, want web alone: admin runs as a container, which its edge pairs with %s", fake.RouterRelay, relayed, fake.RouterDirect)
	}
	if direct["admin"] == "" || direct["web"] != "" {
		t.Errorf("the %s router serves %v, want admin alone", fake.RouterDirect, direct)
	}

	state := readStack(t, p, environment.TierProduction, "shop")
	if state.Apps["web"] != fake.RouterRelay || state.Apps["admin"] != fake.RouterDirect {
		t.Errorf("the edge state pairs %v, want web with %s and admin with %s", state.Apps, fake.RouterRelay, fake.RouterDirect)
	}
	if own := state.Routers[fake.RouterDirect]; own.Slug != "shop" || own.Tier != environment.TierProduction {
		t.Errorf("the edge state records the %s router as %+v, want its own stack of shop in production: it keeps no state inside the edge's", fake.RouterDirect, own)
	}

	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the second Deploy() = %q", second.GetError())
	}
	moved := planes.DataPlane(fake.RouterDirect).Builds("shop", environment.TierProduction, router.DefaultPointer)
	if moved["admin"] == direct["admin"] {
		t.Errorf("the %s router still serves admin's build %q after a promote of a new one: a promote flips every router its apps pair with", fake.RouterDirect, moved["admin"])
	}
	if planes.DataPlane(fake.RouterRelay).Builds("shop", environment.TierProduction, router.DefaultPointer)["web"] == "" {
		t.Errorf("the %s router serves no build of web after the second promote", fake.RouterRelay)
	}
}

func TestAContainerAppsHostnameIsClaimedOnItsOwnRouterAndForwardedByTheEdgeToItsOrigin(t *testing.T) {
	client, p := mixedServed(t)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	if !slices.ContainsFunc(direct.Claims(), func(claim router.Claim) bool { return claim.Hostname == adminHost && claim.App == "admin" }) {
		t.Errorf("the %s router took claims %+v, want %s for admin", fake.RouterDirect, direct.Claims(), adminHost)
	}
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	bound := slices.IndexFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == adminHost })
	if bound < 0 || relay.Bindings()[bound].Origin == nil || *relay.Bindings()[bound].Origin != fake.Origin(fake.RouterDirect) {
		t.Errorf("the edge was bound with %+v, want %s forwarded to %+v, the origin its router answers on", relay.Bindings(), adminHost, fake.Origin(fake.RouterDirect))
	}
	stacks := relay.Stacks()
	if domains := stacks[len(stacks)-1].Domains; slices.Contains(domains, adminHost) || !slices.Contains(domains, "shop.example") {
		t.Errorf("the edge reconciled domains %v, want shop.example and not %s: the edge runs its code on the hostnames its own router answers, and forwards the rest", domains, adminHost)
	}
	if host := readStack(t, p, environment.TierProduction, "shop").Host(adminHost); !host.Probe.OK || host.Probe.Router != fake.RouterDirect {
		t.Errorf("%s is recorded %+v, want it answered by %s", adminHost, host, fake.RouterDirect)
	}
	if !slices.Contains(servedURLs(result), "https://"+adminHost) {
		t.Errorf("the deploy served %v, want https://%s among them", servedURLs(result), adminHost)
	}
}

func TestAContainerAppsNeedsAreCheckedAgainstTheRouterItPromotesThrough(t *testing.T) {
	client, p := mixedServed(t)
	p.Edges().(*fake.Edges).Edge(fake.KindDirect).RouterServes([]edge.Need{edge.NeedStreaming})
	declaresNeed(t, "web", edge.NeedEdgeMiddleware)
	declaresNeed(t, "admin", edge.NeedEdgeMiddleware)

	result, _ := deploy(t, client, mixedRequest())
	if result.GetSuccess() || !strings.Contains(result.GetError(), "app admin needs "+string(edge.NeedEdgeMiddleware)) {
		t.Fatalf("Deploy() = %q, want admin refused for %s: its router forwards to the container and runs none of the edge's code, while web's router does", result.GetError(), edge.NeedEdgeMiddleware)
	}
}

func TestAContainerAppsPreviewBehindAnEdgeRunningCodeIsForwardedUnderItsOwnHostAndGoneWithThePreview(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminDeploymentID))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}
	host := edge.ProjectPreview("preview.example").Host("pr-7", "admin")
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	claimed := slices.IndexFunc(direct.Claims(), func(claim router.Claim) bool { return claim.Hostname == host })
	if claimed < 0 || direct.Claims()[claimed].Pointer != "pr-7" || direct.Claims()[claimed].App != "admin" {
		t.Fatalf("the %s router took claims %+v, want %s claimed for admin on pr-7", fake.RouterDirect, direct.Claims(), host)
	}
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	bound := slices.IndexFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == host })
	if bound < 0 || relay.Bindings()[bound].Origin == nil {
		t.Fatalf("the edge was bound with %+v, want %s forwarded to its router's origin", relay.Bindings(), host)
	}
	served := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Builds("shop", environment.TierPreview, "pr-7")
	if served["admin"] == "" {
		t.Errorf("the %s router serves %v on pr-7, want admin's build", fake.RouterDirect, served)
	}

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if removed, err := drain(stream); err != nil || !removed.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", removed.GetError(), err)
	}
	state := readStack(t, p, environment.TierPreview, "shop")
	if slices.Contains(state.Edge.Bound, host) {
		t.Errorf("the edge still binds %v after pr-7 was removed, want %s unbound with it", state.Edge.Bound, host)
	}
	if _, kept := state.Hosts[host]; kept {
		t.Errorf("the edge state still records %s after pr-7 was removed", host)
	}
	if left := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Builds("shop", environment.TierPreview, "pr-7"); len(left) != 0 {
		t.Errorf("the %s router still serves %v on pr-7 after it was removed", fake.RouterDirect, left)
	}
}
