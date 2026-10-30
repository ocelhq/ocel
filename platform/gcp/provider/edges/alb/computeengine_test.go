package alb

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func readingAddresses(t *testing.T, addresses map[string]string) func(context.Context) (*gcloud.Clients, error) {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		address, found := addresses[name]
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/global/addresses/") || !found {
			w.WriteHeader(http.StatusNotFound)
			googletest.WriteBody(w, map[string]any{"error": map[string]any{"code": http.StatusNotFound, "message": "not found"}})
			return
		}
		googletest.WriteBody(w, &compute.Address{Name: name, Address: address})
	}))
	t.Cleanup(served.Close)
	return googletest.Opener(googletest.Clients(served.URL))
}

func soleDeclared(t *testing.T, seen map[string]declaration, token string) string {
	t.Helper()
	var named []string
	for name, declared := range seen {
		if declared.Token == token {
			named = append(named, name)
		}
	}
	if len(named) != 1 {
		t.Fatalf("the load balancer declares %v of %s, want one", named, token)
	}
	return named[0]
}

func TestTheComputeStacksReadTheLoadBalancerTheEngineRaised(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	if _, err := balancer.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	front := w.declarations(albresources.LoadBalancerStack(environment.TierProduction))
	address := soleDeclared(t, front, "gcp:compute/globalAddress:GlobalAddress")
	want := map[string]string{
		albresources.OutputAddress:        loadBalancerAddress,
		albresources.OutputCertificateMap: soleDeclared(t, front, "gcp:certificatemanager/certificateMap:CertificateMap"),
		albresources.OutputURLMap:         soleDeclared(t, front, "gcp:compute/uRLMap:URLMap"),
		albresources.OutputNotFound:       soleDeclared(t, front, backendToken),
		albresources.OutputAssetsBackend:  soleDeclared(t, front, "gcp:compute/backendBucket:BackendBucket"),
	}

	stacks := ComputeStacks{OpenClients: readingAddresses(t, map[string]string{address: loadBalancerAddress})}
	outputs, err := stacks.Outputs(context.Background(), albresources.Stack{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("Outputs() = %v", err)
	}
	if !maps.Equal(outputs, want) {
		t.Errorf("Outputs() = %v, want %v: an endpoint must route through the very resources the engine raised", outputs, want)
	}
	if !loadBalancerOf(outputs).provisioned() {
		t.Error("the load balancer read reads as not provisioned, and every route would be refused as needing `ocel bootstrap`")
	}
}

func TestTheComputeStacksReadNoLoadBalancerWhereItsAddressIsAbsent(t *testing.T) {
	t.Parallel()

	stacks := ComputeStacks{OpenClients: readingAddresses(t, nil)}
	outputs, err := stacks.Outputs(context.Background(), albresources.Stack{Tier: environment.TierPreview})
	if err != nil || len(outputs) != 0 {
		t.Errorf("Outputs() = %v, %v, want none and no error: an unbootstrapped tier reads as one with no load balancer", outputs, err)
	}
}

func refusedAsNeedingARunner(t *testing.T, call string, err error) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNeedsRunner {
		t.Errorf("%s = %v, want a %s refusal: only a provisioning engine does this", call, err, refusal.CodeNeedsRunner)
	}
}

func TestTheComputeEngineRefusesEverythingThatProvisionsAsNeedingARunner(t *testing.T) {
	t.Parallel()

	engine := NewComputeEngine(readingAddresses(t, nil))
	ctx := context.Background()
	tier := environment.TierProduction
	binding := albresources.Stack{Tier: tier, Slug: "shop"}

	_, err := engine.Stacks.UpLoadBalancer(ctx, albresources.LoadBalancerSpec{Tier: tier}, progress.Discard())
	refusedAsNeedingARunner(t, "Stacks.UpLoadBalancer()", err)
	_, err = engine.Stacks.UpBinding(ctx, albresources.BindingSpec{Tier: tier, Slug: "shop"}, progress.Discard())
	refusedAsNeedingARunner(t, "Stacks.UpBinding()", err)
	_, err = engine.Stacks.UpGate(ctx, tier, nil, progress.Discard())
	refusedAsNeedingARunner(t, "Stacks.UpGate()", err)
	refusedAsNeedingARunner(t, "Stacks.Destroy()", engine.Stacks.Destroy(ctx, binding, progress.Discard()))
	_, err = engine.Stacks.Outputs(ctx, albresources.Stack{Tier: tier, Gate: true})
	refusedAsNeedingARunner(t, "Stacks.Outputs(gate)", err)
	_, err = engine.Images.Ensure(ctx, tier, nil)
	refusedAsNeedingARunner(t, "Images.Ensure()", err)
	refusedAsNeedingARunner(t, "Images.Delete()", engine.Images.Delete(ctx, tier))
	refusedAsNeedingARunner(t, "FrontPart.Claim()", engine.FrontPart.Claim(ctx, tier, "shop"))
	refusedAsNeedingARunner(t, "FrontPart.Release()", engine.FrontPart.Release(ctx, tier, "shop"))
	_, err = engine.EvaluateFront(ctx, tier)
	refusedAsNeedingARunner(t, "EvaluateFront()", err)
	_, err = engine.ReadApps(ctx, tier, "shop")
	refusedAsNeedingARunner(t, "ReadApps()", err)
}

func stagedWeb(t *testing.T, bound edge.EdgeStack) {
	t.Helper()
	for _, build := range []struct{ identity, revision string }{{"b1", "web-00001-abc"}, {"b2", "web-00002-def"}} {
		if err := openRouter(bound).Ledger.PutStaged(context.Background(), router.ReleaseRecord{
			App: "web", Release: build.identity, StorageCoordinate: storageCoordinateOf("web", build.identity), Physical: "ocel-shop-prod-web",
			Revisions: map[string]string{"ocel-shop-prod-web": build.revision},
		}); err != nil {
			t.Fatalf("PutStaged(%s) = %v", build.identity, err)
		}
	}
}

func boundShop(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	balancer, w, bound := reconciled(t)
	if err := bound.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web", Certificate: "projects/acme-prod/locations/global/certificates/shop"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	stagedWeb(t, bound)
	return balancer, w, bound
}

func withoutEngine(t *testing.T, balancer *Edge, bound edge.EdgeStack) edge.EdgeStack {
	t.Helper()
	names := albresources.LoadBalancerNames("ocel", environment.TierProduction)
	deps := balancer.deps
	deps.Engine = NewComputeEngine(readingAddresses(t, map[string]string{names.Address: loadBalancerAddress}))
	stack, err := New(deps).Open(bound.State())
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	return stack
}

func promotionOf(id, identity string) router.PointerMove {
	return router.PointerMove{Promotion: router.Promotion{PromotionID: id, Releases: map[string]string{"web": identity}}}
}

func TestARollbackOnAServingProjectRunsWithNoProvisioningEngine(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w, bound := boundShop(t)
	if err := openRouter(bound).MovePointer(ctx, promotionOf("p1", "b1"), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	stack := withoutEngine(t, balancer, bound)
	raised := len(w.raised())

	for _, step := range []struct{ id, identity string }{{"p2", "b2"}, {"p3", "b1"}} {
		if err := openRouter(stack).MovePointer(ctx, promotionOf(step.id, step.identity), progress.Discard()); err != nil {
			t.Fatalf("MovePointer(%s) with no provisioning engine = %v, want the move made: once a hostname serves its app, a promotion or rollback pins traffic and routes the url map, and provisions nothing", step.id, err)
		}
	}
	if got := w.pins(); len(got) < 3 || got[len(got)-1] != "ocel-shop-prod-web@web-00001-abc" {
		t.Errorf("the moves pinned %v, want the rollback to b1's revision last", got)
	}
	if got := len(w.raised()); got != raised {
		t.Errorf("the moves raised %d stacks, want none", got-raised)
	}
}

func TestAMoveThatWouldRaiseAHostsBackendRefusesBeforeItMovesTrafficWithNoProvisioningEngine(t *testing.T) {
	t.Parallel()

	balancer, w, bound := boundShop(t)
	stack := withoutEngine(t, balancer, bound)

	err := openRouter(stack).MovePointer(context.Background(), promotionOf("p1", "b1"), progress.Discard())
	refusedAsNeedingARunner(t, "MovePointer(p1) onto a hostname with no backend yet", err)
	if pinned := w.pins(); len(pinned) != 0 {
		t.Errorf("the refused move pinned %v, want no traffic moved: a move refused halfway leaves the app on a release its hostname does not route to", pinned)
	}
}
