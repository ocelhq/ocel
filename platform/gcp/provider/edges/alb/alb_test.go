package alb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

func fronting(t *testing.T) (*Edge, *world) {
	t.Helper()
	w := newWorld()
	return New(Deps{
		Records: fake.NewRecords(),
		Stacks:  w,
		Routes:  w,
		Entries: w,
		Pins:    w,
		Project: "acme-prod",
		Region:  "europe-west1",
	}), w
}

func TestTheAlbEdgeIsAnEdge(t *testing.T) {
	t.Parallel()

	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			front, _ := fronting(t)
			return front, edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}
		},
		Hostname: "shop.example.com",
	})
}

func reconciled(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	front, w := fronting(t)
	stack, err := front.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return front, w, stack
}

func TestBindingAHostnameRaisesTheProjectsStackAndRoutesItThroughTheClassUrlMap(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: "shop.example.com", App: "web", Certificate: "projects/acme-prod/locations/global/certificates/shop",
	}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	want := BindingStack("shop", providerkit.ClassProduction)
	if got := w.raised(); !slices.Contains(got, want) {
		t.Errorf("the bind raised %v, want %q among them: a hostname's certificate, neg and backend are one stack per project and class", got, want)
	}
	routed := w.hosts("ocel-alb-production-routes")
	if backend, held := routed["shop.example.com"]; !held || backend == "" {
		t.Errorf("the class url map routes %v, want shop.example.com onto the backend the bind stood up", routed)
	}
	if front := stack.State().Fronts["shop.example.com"]; front != frontAddress {
		t.Errorf("the bind published %q as the front, want the load balancer's address %q for DNS to point at", front, frontAddress)
	}
}

func TestUnbindingTheLastHostnameTakesTheProjectsStackDownRatherThanLeavingItStanding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	if err := stack.UnbindDomain(ctx, "shop.example.com"); err != nil {
		t.Fatalf("UnbindDomain = %v", err)
	}

	want := BindingStack("shop", providerkit.ClassProduction)
	if got := w.torn(); !slices.Contains(got, want) {
		t.Errorf("unbinding the last hostname destroyed %v, want %q among them: bytes a deploy leaves behind after teardown must be zero", got, want)
	}
	if routed := w.hosts("ocel-alb-production-routes"); len(routed) != 0 {
		t.Errorf("the class url map still routes %v after the only hostname was released", routed)
	}
}

func TestAPromotionUnderTheLoadBalancerPinsCloudRunBecauseTheUrlMapNeverMoves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	for _, build := range []struct{ identity, revision string }{{"b1", "web-00001-abc"}, {"b2", "web-00002-def"}} {
		if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
			App: "web", Identity: build.identity, Physical: "ocel-shop-prod-web",
			Revisions: map[string]string{"ocel-shop-prod-web": build.revision},
		}); err != nil {
			t.Fatalf("PutStaged(%s) = %v", build.identity, err)
		}
	}
	for _, step := range []struct{ id, identity string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		err := stack.Promote(ctx, edge.Promotion{PromotionID: step.id, Builds: map[string]string{"web": step.identity}},
			"", edge.DiscardReporter())
		if err != nil {
			t.Fatalf("Promote(%s) = %v", step.id, err)
		}
	}

	want := []string{"ocel-shop-prod-web@web-00001-abc", "ocel-shop-prod-web@web-00002-def", "ocel-shop-prod-web@web-00001-abc"}
	if got := w.pins(); !slices.Equal(got, want) {
		t.Errorf("the promotions pinned %v, want %v: this edge writes no host rule on promote, so the flip is the traffic pin or it is nothing", got, want)
	}
}

func TestAProjectReconciledBeforeItsClassHasALoadBalancerIsToldToBootstrapIt(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	delete(w.outputs, FrontStack(providerkit.ClassProduction))

	var refusal providerkit.Refusal
	_, err := front.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Class: providerkit.ClassProduction}, edge.StackState{})
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Reconcile with no front standing = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "$18") {
		t.Errorf("the refusal reads %q, and the consent for a standing cost is the price said out loud", refusal.Message)
	}
}

func TestAPreviewHostnameIsRefusedWhileTheWildcardIsBeingSpiked(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	var refusal providerkit.Refusal
	_, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: "preview.example.com"})
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("ReconcilePreviewWildcard = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
	if err := front.DestroyPreviewWildcard(context.Background(), "preview.example.com"); err != nil {
		t.Errorf("DestroyPreviewWildcard = %v, want nothing to take down for a wildcard nothing claimed", err)
	}
}

func TestARemovalPlanNamesTheStandingCostItLeavesBehind(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	groups := front.ProjectRemovals(edge.ProjectScope{
		Slug: "shop", Class: providerkit.ClassProduction, Hostnames: []string{"shop.example.com"},
	})
	var kept edge.PlanGroup
	for _, group := range groups {
		if group.Action == edge.PlanKeep {
			kept = group
		}
	}
	if kept.Reason == "" {
		t.Fatalf("ProjectRemovals = %+v, want a kept group saying what stands and keeps costing", groups)
	}
	if !strings.Contains(kept.Reason, "$18") {
		t.Errorf("the kept group reads %q, want the standing cost named: the load balancer outlives the project it fronted", kept.Reason)
	}
}

func TestTheFrontIsNotTakenDownWhileAHostnameIsStillEnteredInItsCertificateMap(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	w.enter("ocel-alb-production-certs", "shop.example.com")

	var refusal providerkit.Refusal
	err := front.Teardown(context.Background(), providerkit.ClassProduction)
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Teardown with a hostname still bound = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
	if !strings.Contains(refusal.Message, "shop.example.com") {
		t.Errorf("the refusal reads %q, want the hostnames that hold the map named: they are what the operator has to release", refusal.Message)
	}
	if got := w.torn(); len(got) != 0 {
		t.Errorf("the refused teardown destroyed %v: a certificate map with entries cannot be deleted, so the destroy fails partway "+
			"and orphans the forwarding rule and the address it had already reached", got)
	}
}

func TestTheFrontComesDownOnceNothingIsBoundToIt(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	if err := front.Teardown(context.Background(), providerkit.ClassProduction); err != nil {
		t.Fatalf("Teardown = %v", err)
	}
	if want := FrontStack(providerkit.ClassProduction); !slices.Contains(w.torn(), want) {
		t.Errorf("the teardown destroyed %v, want %q among them", w.torn(), want)
	}
}

func TestAHostnameBoundBeforeItsAppReleasedIsRoutedToTheFrontsNotFoundBackend(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != notFoundBackend {
		t.Errorf("the class url map routes shop.example.com onto %q, want the front's not-found backend %q: the bind declared no backend "+
			"for an app that has released nothing, and Compute rejects a url map naming a backend that is not there", got, notFoundBackend)
	}
}
