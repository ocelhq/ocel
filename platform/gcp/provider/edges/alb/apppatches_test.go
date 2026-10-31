package alb

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func armored(policy string) transform.Result {
	return transform.Result{Patches: transform.Patches{albresources.SurfaceBackendService: {"securityPolicy": policy}}}
}

func readingApps(balancer *Edge, apps *AppPatches) {
	balancer.deps.Engine.ReadApps = apps.Read
}

func servedWeb(t *testing.T, stack edge.EdgeStack, hostname string) {
	t.Helper()
	ctx := context.Background()
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", StorageCoordinate: storageCoordinateOf("web", "b1"), Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote = %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname, App: "web"}); err != nil {
		t.Fatalf("BindDomain(%s) = %v", hostname, err)
	}
}

func webBackend(w *world, hostname string) declaration {
	return w.declarations(albresources.BindingStack("shop", environment.TierProduction))[must(albresources.BackendName("ocel", "shop", environment.TierProduction, hostname))]
}

func TestADeployWhoseAppPatchesChangedProvisionsItsHostnamesBackendsAgain(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	apps := &AppPatches{}
	readingApps(balancer, apps)
	var said []string
	stack := reconcileShop(t, balancer, &said)
	servedWeb(t, stack, "shop.example.com")
	raised := len(w.raised())

	apps.Set(environment.TierProduction, "shop", "web", armored("web-armor"))
	stack, err := balancer.Reconcile(context.Background(), edge.StackSpec{
		Slug: "shop", Tier: environment.TierProduction, Say: func(line string) { said = append(said, line) },
	}, stack.State())
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := webBackend(w, "shop.example.com").Patch["securityPolicy"]; got != "web-armor" {
		t.Errorf("shop.example.com's backend carries %v, want the security policy web's deploy evaluated", webBackend(w, "shop.example.com").Patch)
	}
	if got := w.raised()[raised:]; !slices.Contains(got, albresources.BindingStack("shop", environment.TierProduction)) {
		t.Errorf("the deploy raised %v, want the project's binding stack raised again with the patch", got)
	}
	want := "Applying the transforms that patch the alb edge resources of web: they differ from the ones they were provisioned with"
	if !slices.Contains(said, want) {
		t.Errorf("the deploy said %q, want %q", said, want)
	}

	raised = len(w.raised())
	if _, err := balancer.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, stack.State()); err != nil {
		t.Fatal(err)
	}
	if got := w.raised()[raised:]; slices.Contains(got, albresources.BindingStack("shop", environment.TierProduction)) {
		t.Errorf("a deploy with unchanged patches raised %v, want the binding stack left as it is", got)
	}
}

func TestAHostnameBoundLaterCarriesThePatchesItsAppsLastDeployEvaluated(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	apps := &AppPatches{}
	apps.Set(environment.TierProduction, "shop", "web", armored("web-armor"))
	readingApps(balancer, apps)
	var said []string
	stack := reconcileShop(t, balancer, &said)

	reopened, err := balancer.Open(stack.State())
	if err != nil {
		t.Fatal(err)
	}
	balancer.deps.Engine.ReadApps = (&AppPatches{}).Read
	servedWeb(t, reopened, "shop.example.com")

	if got := webBackend(w, "shop.example.com").Patch["securityPolicy"]; got != "web-armor" {
		t.Errorf("a hostname bound outside a deploy got %v, want the patch recorded with the project's edge: a raise that drops it reverts the deploy", webBackend(w, "shop.example.com").Patch)
	}
}

func TestRemovingAnAppsPatchProvisionsItsBackendsWithoutIt(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	apps := &AppPatches{}
	apps.Set(environment.TierProduction, "shop", "web", armored("web-armor"))
	readingApps(balancer, apps)
	var said []string
	shop := reconcileShop(t, balancer, &said)
	servedWeb(t, shop, "shop.example.com")

	apps.Set(environment.TierProduction, "shop", "web", transform.Result{})
	shop, err := balancer.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, shop.State())
	if err != nil {
		t.Fatal(err)
	}

	if got := webBackend(w, "shop.example.com").Patch; got != nil {
		t.Errorf("shop.example.com's backend still carries %v, want ocel's own backend once the patch is gone", got)
	}
	reopened, err := balancer.Open(shop.State())
	if err != nil {
		t.Fatal(err)
	}
	if recorded := reopened.(*stack).recorded.Patches; len(recorded) != 0 {
		t.Errorf("the project's edge still records %v, want nothing", recorded)
	}
}

func TestAnAppsPatchesAreKeptApartPerProjectAndTier(t *testing.T) {
	t.Parallel()

	apps := &AppPatches{}
	apps.Set(environment.TierProduction, "shop", "web", armored("shop"))
	apps.Set(environment.TierPreview, "shop", "web", armored("preview"))
	apps.Set(environment.TierProduction, "store", "web", armored("store"))

	read, _ := apps.Read(context.Background(), environment.TierProduction, "shop")
	if len(read) != 1 || read["web"].Patches[albresources.SurfaceBackendService]["securityPolicy"] != "shop" {
		t.Errorf("Read(production, shop) = %v, want only shop's production patch", read)
	}
}
