package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

type defaultingTo struct {
	*fake.Provider
	kind edge.Kind
}

func (d defaultingTo) Facts() provider.Facts {
	facts := d.Provider.Facts()
	facts.DefaultEdge = d.kind
	return facts
}

func TestADeployNamingNoEdgeGoesToTheEdgeTheProvidersFactsDefaultTo(t *testing.T) {
	builtProject(t)
	vendor := defaultingTo{Provider: fake.NewProvider(fake.Options{}), kind: fake.KindDirect}
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindDirect }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge Facts().DefaultEdge names", fronts, fake.KindDirect)
	}
}

func noteOf(result *progressv1.OperationResult) string {
	return strings.Join(result.GetUrlNotes(), "\n")
}

func TestADeployOpensItsDNSForTheEdgeTheProviderDefaultsTo(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindRelay }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge the provider defaults to", fronts, fake.KindRelay)
	}
}

func TestADeployPassesOnAWarningTheEdgeRaisesWhileItReconciles(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).WarnOnReconcile("shop.a.acme.com is more than one label below acme.com")

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a warning to leave the deploy standing", result.GetError())
	}
	if !slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetLevel() == progressv1.Level_LEVEL_WARN && strings.Contains(event.GetMessage(), "more than one label below")
	}) {
		t.Error("the deploy dropped the warning the edge raised while it reconciled")
	}
}

func removePersistentPr7(t *testing.T, client contractv1connect.ProviderServiceClient) *progressv1.OperationResult {
	t.Helper()
	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Error(err)
		return nil
	}
	removed, err := drain(removing)
	if err != nil {
		t.Error(err)
	}
	return removed
}

func TestRemovingAPreviewWhileItsDeployPromotesIsRefusedAndTheDeployLands(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	if first, _ := deploy(t, client, previewRequest()); !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	var removed *progressv1.OperationResult
	plane.BeforeNextPointerMove(func() {
		plane.BeforeNextPointerMove(func() {
			removed = removePersistentPr7(t, client)
		})
	})
	result, _ := deploy(t, client, previewRequest())

	if removed.GetSuccess() || !strings.Contains(removed.GetError(), "a deploy to pr-7 is running: remove it again once it ends") {
		t.Errorf("RemoveEnvironment() between the promote and the deployment hostname's move = %q, want it refused while the deploy holds pr-7", removed.GetError())
	}
	if !result.GetSuccess() {
		t.Errorf("Deploy() = %q, want it to land: the removal never ran", result.GetError())
	}
}

func TestRemovingAPreviewWhileItsDeployProvisionsIsRefusedAndTheDeployLands(t *testing.T) {
	for name, existing := range map[string]bool{"on its first deploy": false, "on a deploy of an existing preview": true} {
		t.Run(name, func(t *testing.T) {
			builtProject(t)
			client, vendor := deployServed(t)
			previewBootstrapped(t, client)
			if existing {
				if first, _ := deploy(t, client, previewRequest()); !first.GetSuccess() {
					t.Fatalf("Deploy() = %q", first.GetError())
				}
			}
			var removed *progressv1.OperationResult
			vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
				if spec.App != nil && removed == nil {
					removed = removePersistentPr7(t, client)
				}
				return nil
			})

			result, _ := deploy(t, client, previewRequest())

			if removed.GetSuccess() || !strings.Contains(removed.GetError(), "a deploy to pr-7 is running") {
				t.Errorf("RemoveEnvironment() while pr-7 provisioned = %q, want it refused while the deploy holds pr-7", removed.GetError())
			}
			if !result.GetSuccess() {
				t.Errorf("Deploy() = %q, want it to land: the removal never ran", result.GetError())
			}
			if !isPreviewRecorded(t, vendor) {
				t.Error("pr-7 is not recorded after its deploy landed")
			}
		})
	}
}

func TestAPreviewOnARouterThatAddressesItselfDeploysWithNoPreviewDomainAndAnnouncesItsOwnURL(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the preview released on the address its router gives it", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !strings.HasSuffix(urls[0], ".fake.invalid") {
		t.Errorf("the preview deploy announced %v, want the one url its release recorded: the router answers on it and no hostname is bound", urls)
	}
}

func TestAProductionReleaseOnARouterThatAddressesItselfAnnouncesItsOwnURL(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
	req := deployRequest()
	req.Manifest.Domains = nil
	req.Edge = edged(fake.KindRelay, "")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasSuffix(urls[0], ".fake.invalid") {
		t.Errorf("the deploy announced %v, want the one url its release recorded", urls)
	}
}

func TestAPreviewOnARouterThatAddressesItselfServesEachDeploymentOnItsOwnURLUntilAPruneDropsIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	first, _ := deploy(t, client, previewDeployRequest())
	deploy(t, client, previewDeployRequest())
	second, _ := deploy(t, client, previewDeployRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	firstDeployment, secondDeployment := findDeploymentURL(first, "web"), findDeploymentURL(second, "web")
	if !strings.Contains(firstDeployment, fake.DeploymentURLMarker) || firstDeployment == secondDeployment {
		t.Fatalf("the deploys announced deployment urls %q and %q, want each deploy its own url", firstDeployment, secondDeployment)
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	want := router.FormatDeploymentPointer("pr-7", first.GetPromotionId())
	if pointer, builds := plane.FindServingPointer(host(firstDeployment)); pointer != want || builds["web"] == "" {
		t.Errorf("the first deployment's url is served by %q with %v after the next deploy, want %s still serving it", pointer, builds, want)
	}

	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewDeployRequest().GetEnvironment(), Edge: previewDeployRequest().GetEdge(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}
	if pointer, _ := plane.FindServingPointer(host(firstDeployment)); pointer != "" {
		t.Errorf("the pruned deployment's url is still served by %q, want nothing", pointer)
	}
	if pointer, _ := plane.FindServingPointer(host(secondDeployment)); pointer == "" {
		t.Errorf("%s stopped serving, want the deployment the prune kept still served", secondDeployment)
	}
}

func TestAPartAnEdgeAddsDuringAReconcileIsSaidAtInfoLevel(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	relayEdge(vendor).OnReconcile(func(spec edge.StackSpec) {
		if spec.Say != nil {
			spec.Say("added the kit part")
		}
	})

	result, events := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if !slices.Contains(said(events, progressv1.Level_LEVEL_INFO), "added the kit part") {
		t.Errorf("the deploy said %v at info level, want the part the edge added: adding one is news, not a warning", said(events, progressv1.Level_LEVEL_INFO))
	}
}
