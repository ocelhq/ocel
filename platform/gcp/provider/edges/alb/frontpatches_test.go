package alb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func sslPatched(policy string) transform.Result {
	return transform.Result{Patches: transform.Patches{surfaceHTTPSProxy: {"sslPolicy": policy}}}
}

func evaluatingFront(balancer *Edge, result *transform.Result) {
	balancer.deps.EvaluateFront = func(context.Context, environment.Tier) (transform.Result, error) {
		return *result, nil
	}
}

func frontUps(w *world) int {
	ups := 0
	for _, stack := range w.raised() {
		if stack == LoadBalancerStack(environment.TierProduction) {
			ups++
		}
	}
	return ups
}

func reconcileShop(t *testing.T, balancer *Edge, said *[]string) edge.EdgeStack {
	t.Helper()
	return reconcileProject(t, balancer, "shop", said)
}

func reconcileProject(t *testing.T, balancer *Edge, slug string, said *[]string) edge.EdgeStack {
	t.Helper()
	stack, err := balancer.Reconcile(context.Background(), edge.StackSpec{
		Slug: slug, Tier: environment.TierProduction,
		Say: func(line string) { *said = append(*said, line) },
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(%s) = %v", slug, err)
	}
	return stack
}

func TestADeployWhoseFrontPatchesDifferFromTheLoadBalancersProvisionsItAgainWithThem(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)

	var said []string
	reconcileShop(t, balancer, &said)

	if got := frontUps(w); got != 1 {
		t.Fatalf("the deploy raised the load balancer %d times, want once: it carries none of the patches this deploy evaluated", got)
	}
	proxy := w.declarations(LoadBalancerStack(environment.TierProduction))["ocel-prod-alb-https"]
	if got := proxy.Patch["sslPolicy"]; got != "modern-tls" {
		t.Errorf("the https proxy was raised with %v, want the patch's ssl policy", proxy.Patch)
	}
	want := "Applying the transforms that patch the alb load balancer of the production tier: they differ from the ones it was provisioned with"
	if !slices.Contains(said, want) {
		t.Errorf("the deploy said %q, want %q", said, want)
	}

	reconcileShop(t, balancer, &said)
	if got := frontUps(w); got != 1 {
		t.Errorf("a second deploy with the same patches raised the load balancer again (%d raises), want it left as it is", got)
	}
}

func TestADeployOfAnotherProjectWithNoFrontPatchesLeavesTheLoadBalancerAsItIsPatched(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	var said []string
	reconcileShop(t, balancer, &said)

	evaluated = transform.Result{}
	reconcileProject(t, balancer, "blog", &said)
	if got := frontUps(w); got != 1 {
		t.Errorf("a deploy of blog with no patches raised the load balancer again (%d raises), want it left with the ones shop gave it", got)
	}

	if _, err := balancer.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	proxy := w.declarations(LoadBalancerStack(environment.TierProduction))["ocel-prod-alb-https"]
	if got := proxy.Patch["sslPolicy"]; got != "modern-tls" {
		t.Errorf("raising the load balancer with nothing evaluated applied %v, want the patches it records: a bootstrap names no project whose patches it could drop", proxy.Patch)
	}
}

func TestADeployOfTheProjectThatPatchedTheFrontWithNoPatchesPutsTheLoadBalancerBack(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	var said []string
	reconcileShop(t, balancer, &said)

	evaluated = transform.Result{}
	reconcileShop(t, balancer, &said)

	if got := frontUps(w); got != 2 {
		t.Fatalf("shop's deploy with its front patch removed raised the load balancer %d times in all, want twice: removing a patch puts its fields back", got)
	}
	if got, set := w.declarations(LoadBalancerStack(environment.TierProduction))["ocel-prod-alb-https"].Patch["sslPolicy"]; set {
		t.Errorf("the https proxy keeps the ssl policy %v, want it put back", got)
	}
	reconcileShop(t, balancer, &said)
	if got := frontUps(w); got != 2 {
		t.Errorf("a further deploy of shop with no patches raised the load balancer again (%d raises), want it left as it is", got)
	}
}

func TestABootstrapBetweenDeploysLeavesTheProjectThatPatchedTheFrontAbleToPutItBack(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	var said []string
	reconcileShop(t, balancer, &said)
	if _, err := balancer.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	evaluated = transform.Result{}
	reconcileShop(t, balancer, &said)

	if got, set := w.declarations(LoadBalancerStack(environment.TierProduction))["ocel-prod-alb-https"].Patch["sslPolicy"]; set {
		t.Errorf("the https proxy keeps the ssl policy %v, want it put back: shop patched it, and a bootstrap names no project to take that over", got)
	}
}

func TestThePlanOfADeployWhoseFrontPatchesDifferSaysTheLoadBalancerIsProvisionedAgain(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)

	group, err := balancer.PlanFrontRepatch(context.Background(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatalf("PlanFrontRepatch() = %v", err)
	}
	if group == nil || group.Action != provider.ActionUpdate || group.Name != LoadBalancerStack(environment.TierProduction) {
		t.Fatalf("PlanFrontRepatch() = %+v, want the tier's load balancer stack updated", group)
	}

	var said []string
	reconcileShop(t, balancer, &said)
	if group, err := balancer.PlanFrontRepatch(context.Background(), environment.TierProduction, "shop"); err != nil || group != nil {
		t.Errorf("PlanFrontRepatch() = %+v, %v once the load balancer records the patches, want nothing planned", group, err)
	}
	evaluated = transform.Result{}
	if group, err := balancer.PlanFrontRepatch(context.Background(), environment.TierProduction, "blog"); err != nil || group != nil {
		t.Errorf("PlanFrontRepatch(blog) = %+v, %v for a deploy of another project with no patches, want nothing planned", group, err)
	}
	if group, err := balancer.PlanFrontRepatch(context.Background(), environment.TierProduction, "shop"); err != nil || group == nil || group.Action != provider.ActionUpdate {
		t.Errorf("PlanFrontRepatch(shop) = %+v, %v once shop drops its patch, want the load balancer provisioned again without it", group, err)
	}
}

type racedStore struct {
	records.Store
	race func(ctx context.Context) error
}

func (s *racedStore) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	if race := s.race; race != nil {
		s.race = nil
		if err := race(ctx); err != nil {
			return "", err
		}
	}
	return s.Store.Write(ctx, entry)
}

func racingFront(balancer *Edge, written transform.Result) {
	store := &racedStore{Store: balancer.deps.KeyValues}
	store.race = func(ctx context.Context) error {
		entry, err := records.ReadOrEmpty(ctx, store.Store, frontPatchesAt(environment.TierProduction))
		if err != nil {
			return err
		}
		entry.Value, err = json.Marshal(patchesOf(written))
		if err != nil {
			return err
		}
		_, err = store.Store.Write(ctx, entry)
		return err
	}
	balancer.deps.KeyValues = store
}

func TestTwoDeploysRecordingTheSameFrontPatchesAtOnceBothSucceed(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	racingFront(balancer, evaluated)

	if _, err := balancer.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{}); err != nil {
		t.Errorf("Reconcile() = %v, want it through: the deploy that recorded first recorded the same patches", err)
	}
}

func TestADeployRecordingFrontPatchesAnotherDeployChangedMeanwhileIsToldToDeployAgain(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	racingFront(balancer, sslPatched("legacy-tls"))

	_, err := balancer.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("Reconcile() = %v, want a %s refusal: the load balancer now carries patches neither deploy can vouch for", err, refusal.CodeBusy)
	}
}

func TestTearingTheLoadBalancerDownForgetsThePatchesItCarried(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	evaluated := sslPatched("modern-tls")
	evaluatingFront(balancer, &evaluated)
	var said []string
	reconcileShop(t, balancer, &said)

	if err := balancer.Teardown(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown() = %v", err)
	}
	entry, err := records.ReadOrEmpty(context.Background(), balancer.deps.KeyValues, frontPatchesAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Value) > 0 {
		t.Errorf("the torn-down load balancer left %s behind, want nothing: a teardown reclaims what the deploys wrote", entry.Value)
	}
}
