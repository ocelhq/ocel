package alb

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func balancing(t *testing.T) (*Edge, *world) {
	t.Helper()
	w := newWorld()
	return New(Deps{
		KeyValues: fake.NewKeyValues(),
		Stacks:    w,
		Routes:    w,
		Entries:   w,
		Pins:      w,
		WarmURL:   w.warmThrough,
		Project:   "acme-prod",
		Region:    "europe-west1",
	}), w
}

func TestTheALBEdgeBehavesAsEveryEdgeMust(t *testing.T) {
	t.Parallel()

	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			balancer, _ := balancing(t)
			return balancer, edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}
		},
		Hostname: "shop.example.com",
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			balancer, _ := balancing(t)
			return balancer, edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, PruneOnly: true}, previewWildcard()
		},
	})
}

const conformanceService = "ocel-shop-production-web"

func TestTheALBRouterBehavesAsEveryRouterMust(t *testing.T) {
	routerconformance.Run(t, routerconformance.Suite{
		New: func(t *testing.T) routerconformance.Fixture {
			balancer, w, stack := reconciled(t)
			w.outputs[ShieldedLoadBalancerStack(environment.TierProduction)] = shieldedLoadBalancer()
			w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedPreviewLoadBalancer()
			state := stack.State()
			return routerconformance.Fixture{
				Router: NewRouter(balancer),
				Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
				Prior:  router.NewStackState(state),
				Serving: func(string) string {
					return strings.TrimPrefix(w.pinnedRevision(conformanceService), "rev-")
				},
				FailNextPointerMove: w.refusePins,
			}
		},
		Previews: func(t *testing.T) routerconformance.Fixture {
			balancer, w, shared := reconciledPreview(t)
			state := shared.State()
			moved := &movedHosts{hosts: map[string][]string{}}
			return routerconformance.Fixture{
				Router: hostRecordingRouter{Router: NewRouter(balancer), moved: moved},
				Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
				Prior:  router.NewStackState(state),
				Serving: func(pointer string) string {
					return strings.TrimPrefix(w.servedRevision(state.Slug, state.Tier, moved.listHostsOf(pointer)), "rev-")
				},
				FailNextPointerMove: w.refusePins,
			}
		},
		Hostname:    "shop.example.com",
		PreviewBase: "preview.example.com",
		Record: func(app, build string) router.ReleaseRecord {
			return router.ReleaseRecord{
				App: app, Release: build, Physical: conformanceService,
				Revisions: map[string]string{conformanceService: "rev-" + build},
			}
		},
	})
}

func reconciled(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	balancer, w := balancing(t)
	stack, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return balancer, w, stack
}

func TestBindingAHostnameRaisesTheProjectsStackAndRoutesItThroughTheTierUrlMap(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: "shop.example.com", App: "web", Certificate: "projects/acme-prod/locations/global/certificates/shop",
	}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	want := BindingStack("shop", environment.TierProduction)
	if got := w.raised(); !slices.Contains(got, want) {
		t.Errorf("the bind raised %v, want %q among them: a hostname's certificate, neg and backend are one stack per project and tier", got, want)
	}
	routed := w.hosts("ocel-alb-production-routes")
	if backend, found := routed["shop.example.com"]; !found || backend == "" {
		t.Errorf("the tier url map routes %v, want shop.example.com onto the backend the bind provisioned", routed)
	}
	if balancer := stack.State().Addresses["shop.example.com"]; balancer != loadBalancerAddress {
		t.Errorf("the bind published %q as the balancer, want the load balancer's address %q for DNS to point at", balancer, loadBalancerAddress)
	}
}

func TestBindingAHostnameTheUrlMapHasNoHostRuleLeftForIsRefusedBeforeAnythingIsRaised(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	w.fillHostRules("ocel-alb-production-routes", maxHostRules)
	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web", Certificate: "projects/acme-prod/locations/global/certificates/shop"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("BindDomain onto a url map with %d host rules = %v, want a refusal", maxHostRules, err)
	}
	if got := w.raised(); slices.Contains(got, BindingStack("shop", environment.TierProduction)) {
		t.Errorf("the refused bind raised %v, want nothing: Compute refuses the host rule only after the neg and backend service exist", got)
	}
}

func TestBindingAHostnameTheProjectHasNoBackendServiceQuotaLeftForIsRefusedBeforeAnythingIsRaised(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	w.setBackendServiceQuota(BackendServiceQuota{Usage: 50, Limit: 50})
	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web", Certificate: "projects/acme-prod/locations/global/certificates/shop"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("BindDomain with every backend service the quota allows in use = %v, want a refusal", err)
	}
	if got := w.raised(); slices.Contains(got, BindingStack("shop", environment.TierProduction)) {
		t.Errorf("the refused bind raised %v, want nothing: Compute refuses the backend service only after the neg exists", got)
	}
}

func TestBindingTheWildcardOntoAFullUrlMapIsNotRefused(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	w.fillHostRules("ocel-alb-production-routes", maxHostRules)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: edge.PreviewWildcard(previewBase), App: "web", Certificate: previewCertificate,
	}); err != nil {
		t.Fatalf("BindDomain(wildcard) onto a full url map = %v, want it bound: a wildcard writes no host rule", err)
	}
}

func TestTwoProjectsClaimingOneHostnameAtOnceLeaveItWithExactlyOne(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	ctx := context.Background()
	stacks := map[string]edge.EdgeStack{}
	for _, slug := range []string{"shop", "store"} {
		stack, err := balancer.Reconcile(ctx, edge.StackSpec{Slug: slug, Tier: environment.TierProduction}, edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile(%s) = %v", slug, err)
		}
		stacks[slug] = stack
	}

	outcomes := make(chan error, len(stacks))
	for _, stack := range stacks {
		go func() {
			outcomes <- stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"})
		}()
	}
	var refused []error
	for range stacks {
		if err := <-outcomes; err != nil {
			refused = append(refused, err)
		}
	}
	if len(refused) != 1 {
		t.Fatalf("binding shop.example.com from two projects at once refused %d of them, want exactly one: %v", len(refused), refused)
	}
	var losing refusal.Refusal
	if !errors.As(refused[0], &losing) {
		t.Errorf("the losing bind failed with %v, want a refusal that says who serves the hostname", refused[0])
	}

	owner, err := balancer.DomainOwner(ctx, "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for slug, stack := range stacks {
		bound := slices.Contains(stack.State().Bound, "shop.example.com")
		if owns := owner == Surface(slug, environment.TierProduction); owns != bound {
			t.Errorf("%s reads as bound=%t while the claim names %q: what the ledger says and what the url map routes must be one project", slug, bound, owner)
		}
	}
	if _, routed := w.hosts("ocel-alb-production-routes")["shop.example.com"]; !routed {
		t.Error("the url map has no rule for shop.example.com after the winning bind")
	}
}

func TestAHostnameAnotherProjectServesIsRefusedRatherThanTakenOver(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	ctx := context.Background()
	shop, err := balancer.Reconcile(ctx, edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shop.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain(shop) = %v", err)
	}
	store, err := balancer.Reconcile(ctx, edge.StackSpec{Slug: "store", Tier: environment.TierProduction}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	before := w.hosts("ocel-alb-production-routes")
	raised := len(w.raised())

	err = store.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, Surface("shop", environment.TierProduction)) {
		t.Fatalf("BindDomain(store) = %v, want a refusal naming the project that serves the hostname", err)
	}
	if after := w.hosts("ocel-alb-production-routes"); !maps.Equal(before, after) {
		t.Errorf("the refused bind rewrote the url map from %v to %v, and a refusal that rerouted is a takeover", before, after)
	}
	if len(w.raised()) != raised {
		t.Errorf("the refused bind raised %v, and a stack for a hostname another project serves is one nothing routes to", w.raised()[raised:])
	}
	if owner, _ := balancer.DomainOwner(ctx, "shop.example.com"); owner != Surface("shop", environment.TierProduction) {
		t.Errorf("the refused bind left the claim naming %q", owner)
	}
}

func TestUnbindingTheLastHostnameTakesTheProjectsStackDownRatherThanLeavingItInPlace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	if err := stack.UnbindDomain(ctx, "shop.example.com"); err != nil {
		t.Fatalf("UnbindDomain = %v", err)
	}

	want := BindingStack("shop", environment.TierProduction)
	if got := w.torn(); !slices.Contains(got, want) {
		t.Errorf("unbinding the last hostname destroyed %v, want %q among them: bytes a deploy leaves behind after teardown must be zero", got, want)
	}
	if routed := w.hosts("ocel-alb-production-routes"); len(routed) != 0 {
		t.Errorf("the tier url map still routes %v after the only hostname was released", routed)
	}
}

func TestAPromotionUnderTheLoadBalancerPinsCloudRunBecauseTheUrlMapNeverMoves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	for _, build := range []struct{ identity, revision string }{{"b1", "web-00001-abc"}, {"b2", "web-00002-def"}} {
		if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
			App: "web", Release: build.identity, Physical: "ocel-shop-prod-web",
			Revisions: map[string]string{"ocel-shop-prod-web": build.revision},
		}); err != nil {
			t.Fatalf("PutStaged(%s) = %v", build.identity, err)
		}
	}
	for _, step := range []struct{ id, identity string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: step.id, Releases: map[string]string{"web": step.identity}}}, progress.Discard())
		if err != nil {
			t.Fatalf("Promote(%s) = %v", step.id, err)
		}
	}

	want := []string{"ocel-shop-prod-web@web-00001-abc", "ocel-shop-prod-web@web-00002-def", "ocel-shop-prod-web@web-00001-abc"}
	if got := w.pins(); !slices.Equal(got, want) {
		t.Errorf("the promotions pinned %v, want %v: this edge writes no host rule on promote, so the pointer move is the traffic pin or it is nothing", got, want)
	}
}

func TestAProjectReconciledBeforeItsTierHasALoadBalancerIsToldToBootstrapIt(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	delete(w.outputs, LoadBalancerStack(environment.TierProduction))

	var refused refusal.Refusal
	_, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Reconcile with no balancer provisioned = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "$18") {
		t.Errorf("the refusal reads %q, and the consent for a recurring cost is the price said out loud", refused.Message)
	}
}

const previewBase = "preview.example.com"

const previewCertificate = "projects/acme-prod/locations/global/certificates/ocel-preview"

func previewWildcard() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{BaseDomain: previewBase, Certificate: previewCertificate}
}

func TestThePreviewWildcardEntersItsCertificateAndRoutesNoHostnameOfItsOwn(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	published, err := balancer.ReconcilePreviewWildcard(context.Background(), previewWildcard())
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if published != loadBalancerAddress {
		t.Errorf("ReconcilePreviewWildcard published %q, want the tier balancer's address %q for DNS to point the wildcard at", published, loadBalancerAddress)
	}

	if routed := w.hosts(tierRoutes); len(routed) != 0 {
		t.Errorf("the tier url map routes %v, want nothing: a preview is answered by the exact host rule its promotion writes, "+
			"and a wildcard rule would reach whatever Cloud Run service a hostname's label names", routed)
	}
	resources := w.declarations(LoadBalancerStack(environment.TierPreview))
	for name, declared := range resources {
		if cloudRunMask(declared) != "" {
			t.Errorf("the preview balancer declares %s with the url mask %q, want none: a mask reaches every service in the project by name", name, cloudRunMask(declared))
		}
	}
	if _, entered := resources[previewEntryName(previewBase)]; !entered {
		t.Errorf("the preview balancer declares %v, want a certificate map entry: nothing terminates TLS for %s without one",
			keys(resources), edge.PreviewWildcard(previewBase))
	}
}

func TestTheWildcardIsOwnedByTheSharedPreviewEntryRatherThanByAProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, _ := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}

	owner, err := balancer.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil {
		t.Fatalf("DomainOwner = %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, want %q: the wildcard is the bootstrap's, and a project that asks whether it may bind it "+
			"has to be told it is already served", edge.PreviewWildcard(previewBase), owner, edge.PreviewEntryOwner)
	}
	if owner, err := balancer.DomainOwner(ctx, edge.PreviewWildcard("other.example.com")); err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing: no wildcard but the one raised is served", edge.PreviewWildcard("other.example.com"), owner, err)
	}
}

func TestAPreviewWildcardWithNoCertificateIsRefusedRatherThanServedOnPlainHttp(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	var refused refusal.Refusal
	_, err := balancer.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: previewBase})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("ReconcilePreviewWildcard with no certificate = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}

func TestRaisingTheTierLoadBalancerAgainKeepsTheWildcardCertificateEntered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if _, err := balancer.Bootstrap(ctx, environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}

	resources := w.declarations(LoadBalancerStack(environment.TierPreview))
	if _, declared := resources[previewEntryName(previewBase)]; !declared {
		t.Errorf("the balancer raised again declares %v, and the wildcard's certificate is gone from it: a bootstrap that reruns would "+
			"take every preview in the tier down", keys(resources))
	}
}

func TestDestroyingThePreviewWildcardTakesItsCertificateAndLeavesTheLoadBalancerInPlace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if err := balancer.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard = %v", err)
	}

	resources := w.declarations(LoadBalancerStack(environment.TierPreview))
	if _, declared := resources[previewEntryName(previewBase)]; declared {
		t.Errorf("the balancer still declares %v after the wildcard was released: bytes a release leaves behind must be zero", keys(resources))
	}
	if slices.Contains(w.torn(), LoadBalancerStack(environment.TierPreview)) {
		t.Error("releasing the wildcard destroyed the tier balancer, which every project in the tier is answered by")
	}
	owner, err := balancer.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing serving a released wildcard", edge.PreviewWildcard(previewBase), owner, err)
	}
	if err := balancer.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Errorf("DestroyPreviewWildcard again = %v, want a release to be re-entrant", err)
	}
}

func TestAWildcardWhoseLoadBalancerFailsToRiseIsOwnedByNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := balancing(t)
	w.breakUp(LoadBalancerStack(environment.TierPreview), errors.New("the preview balancer would not rise"))

	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err == nil {
		t.Fatal("ReconcilePreviewWildcard = nil, want the failure the balancer reported")
	}

	owner, err := balancer.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing: a reconcile that never raised a balancer serves no wildcard, "+
			"and a record claiming it would refuse every project that asks to bind one", edge.PreviewWildcard(previewBase), owner, err)
	}
}

func TestAWildcardWhoseTeardownFailsIsStillOwnedSoTheRetryStillTearsItDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	w.breakUp(LoadBalancerStack(environment.TierPreview), errors.New("the preview balancer would not rise"))

	if err := balancer.DestroyPreviewWildcard(ctx, previewBase); err == nil {
		t.Fatal("DestroyPreviewWildcard = nil, want the failure the balancer reported")
	}

	owner, err := balancer.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, %v, want %q: the certificate map entry is still provisioned, "+
			"and a retry that read the wildcard as gone would leave them there forever",
			edge.PreviewWildcard(previewBase), owner, err, edge.PreviewEntryOwner)
	}
}

func TestThePreviewWildcardIsKeptWhileAProjectIsStillServedOnIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, _ := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	servedOnPreview(t, balancer, "shop", previewBase)

	var refused refusal.Refusal
	err := balancer.DestroyPreviewWildcard(ctx, previewBase)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("DestroyPreviewWildcard with a project still served = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "shop") {
		t.Errorf("the refusal reads %q, want the projects served on it named: they are what the operator has to release", refused.Message)
	}
}

func TestAPreviewAliasIsAnsweredOnAnExactHostRuleOntoItsAppsService(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := balancing(t)
	if _, err := balancer.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	shared, err := balancer.Reconcile(ctx,
		edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, PruneOnly: true},
		edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile = %v", err)
	}
	stack := routerStack{s: shared.(*stack)}
	record := previewRecord("b1")

	move := aliasMove("p1", aliasHost, record)
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(pr-7) = %v", err)
	}

	backend := backendName("shop", environment.TierPreview, aliasHost)
	if got := w.hosts(tierRoutes)[aliasHost]; got != backend {
		t.Errorf("%s is routed to %q, want its own backend %q", aliasHost, got, backend)
	}
	neg := w.declarations(BindingStack("shop", environment.TierPreview))[negName("shop", environment.TierPreview, aliasHost)]
	run, _ := neg.Args["cloudRun"].(map[string]any)
	if run["service"] != previewService || run["tag"] != nil {
		t.Errorf("the alias's neg points at %v, want service %s and no tag: the alias follows the service's traffic, which the promotion pins", run, previewService)
	}
	if got := w.pinnedRevision(previewService); got != record.Revisions[previewService] {
		t.Errorf("the promotion pinned %q, want %s", got, record.Revisions[previewService])
	}
}

func TestAProjectsOwnPreviewWildcardEntersItsCertificateAndEachPreviewIsRoutedExactly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, shared := reconciledPreview(t)
	wildcard := edge.PreviewWildcard("preview.shop.example")
	if err := shared.BindDomain(ctx, edge.DomainBinding{Hostname: wildcard, Certificate: previewCertificate}); err != nil {
		t.Fatalf("BindDomain(%s) = %v", wildcard, err)
	}

	declared := w.declarations(BindingStack("shop", environment.TierPreview))
	entry, entered := declared[entryName("shop", environment.TierPreview, wildcard)]
	if !entered || entry.Args["hostname"] != wildcard {
		t.Fatalf("the binding declares %v, want a certificate map entry for %s: it is what terminates TLS for every preview under it", keys(declared), wildcard)
	}
	if routed, ruled := w.hosts(tierRoutes)[wildcard]; ruled {
		t.Errorf("the tier url map routes %s to %q, want no rule for the wildcard: each preview under it is an exact host rule its promotion writes", wildcard, routed)
	}

	stack := routerStack{s: shared.(*stack)}
	alias := "pr-7-bbbbbbbbbbbbbbbb.preview.shop.example"
	record := previewRecord("b1")
	record.Physical = "ocel-shop-pr-7-web"
	if err := stack.MovePointer(ctx, aliasMove("p1", alias, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(pr-7) = %v", err)
	}
	if got := w.hosts(tierRoutes)[alias]; got != backendName("shop", environment.TierPreview, alias) {
		t.Errorf("%s is routed to %q, want its own backend", alias, got)
	}
	if routed, ruled := w.hosts(tierRoutes)[wildcard]; ruled {
		t.Errorf("the promotion routed the wildcard to %q, want it left to its certificate alone", routed)
	}
}

func TestTheWildcardRemovalPlanNamesWhatComesDownAndWhatStays(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	removed, kept := balancer.PreviewWildcardRemovals(edge.PreviewWildcard(previewBase))
	named := map[string]bool{}
	for _, change := range removed.Changes {
		named[change.Kind] = true
	}
	for _, kind := range []string{
		"certificatemanager.CertificateMapEntry",
	} {
		if !named[kind] {
			t.Errorf("the removal plan names %v, want a %s row: a plan that hides a resource leaves it provisioned and billing", removed.Changes, kind)
		}
	}
	if kept.Action != edge.PlanKeep || kept.Reason == "" {
		t.Errorf("the kept group = %+v, want the balancer kept with a reason", kept)
	}
}

func keys(declarations map[string]declaration) []string {
	return slices.Sorted(maps.Keys(declarations))
}

func cloudRunMask(neg declaration) string {
	run, ok := neg.Args["cloudRun"].(map[string]any)
	if !ok {
		return ""
	}
	mask, _ := run["urlMask"].(string)
	return mask
}

func reconciledPreview(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	balancer, w := balancing(t)
	stack, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierPreview}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return balancer, w, stack
}

func servedOnPreview(t *testing.T, balancer *Edge, slug, base string) {
	t.Helper()
	ctx := context.Background()
	stack, err := balancer.Reconcile(ctx,
		edge.StackSpec{Slug: slug, Tier: environment.TierPreview, PruneOnly: true},
		edge.StackState{GlobalPreview: base})
	if err != nil {
		t.Fatalf("Reconcile(%s) = %v", slug, err)
	}
	state := stackrecords.EdgeState{Kind: Kind, Edge: stack.State()}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	name := stackrecords.EdgeStackKey(environment.TierPreview, slug)
	entry, err := keyvalue.ReadOrEmpty(ctx, balancer.deps.KeyValues, name)
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = encoded
	if _, err := balancer.deps.KeyValues.Write(ctx, entry); err != nil {
		t.Fatal(err)
	}
}

func TestARemovalPlanNamesTheRecurringCostItLeavesBehind(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	groups := balancer.ProjectRemovals(edge.ProjectScope{
		Slug: "shop", Tier: environment.TierProduction, Hostnames: []string{"shop.example.com"},
	})
	var kept edge.PlanGroup
	for _, group := range groups {
		if group.Action == edge.PlanKeep {
			kept = group
		}
	}
	if kept.Reason == "" {
		t.Fatalf("ProjectRemovals = %+v, want a kept group saying what stays provisioned and keeps costing", groups)
	}
	if !strings.Contains(kept.Reason, "$18") {
		t.Errorf("the kept group reads %q, want the recurring cost named: the load balancer outlives the project it served", kept.Reason)
	}
}

func TestTheLoadBalancerIsNotTakenDownWhileAHostnameIsStillEnteredInItsCertificateMap(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	w.enter("ocel-alb-production-certs", "shop.example.com")

	var refused refusal.Refusal
	err := balancer.Teardown(context.Background(), environment.TierProduction)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Teardown with a hostname still bound = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "shop.example.com") {
		t.Errorf("the refusal reads %q, want the hostnames still entered in the map named: they are what the operator has to release", refused.Message)
	}
	if got := w.torn(); len(got) != 0 {
		t.Errorf("the refused teardown destroyed %v: a certificate map with entries cannot be deleted, so the destroy fails partway "+
			"and orphans the forwarding rule and the address it had already reached", got)
	}
}

func TestTheLoadBalancerComesDownOnceNothingIsBoundToIt(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	if err := balancer.Teardown(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown = %v", err)
	}
	if want := LoadBalancerStack(environment.TierProduction); !slices.Contains(w.torn(), want) {
		t.Errorf("the teardown destroyed %v, want %q among them", w.torn(), want)
	}
}

func TestTheFirstReleaseAfterABindTakesTheHostnameLive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	want := backendName("shop", environment.TierProduction, "shop.example.com")
	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != want {
		t.Errorf("the tier url map routes shop.example.com onto %q, want the project's own backend %q: the bind served the hostname a 404 "+
			"because the app had released nothing, and the release that gives it a service is what takes it live", got, want)
	}
}

func TestTheReleaseThatTakesAHostnameLiveSaysWhichServiceItRoutesTo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	progress := &fake.Log{}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	want := "INFO Routing shop.example.com to web's Cloud Run service ocel-shop-prod-web"
	if got := progress.Lines(); !slices.Contains(got, want) {
		t.Errorf("the release said %q, want %q among it", got, want)
	}
}

func TestAHostnameBoundAfterAReleaseIsRoutedToThePromotedService(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote = %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	want := backendName("shop", environment.TierProduction, "shop.example.com")
	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != want {
		t.Errorf("the tier url map routes shop.example.com onto %q, want the project's own backend %q: the release was promoted before the bind, "+
			"and no later promotion comes to take the hostname live", got, want)
	}
}

func TestAPromotionOfAnotherAppLeavesAHostnameServingNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "admin", Release: "b1", Physical: "ocel-shop-prod-admin",
		Revisions: map[string]string{"ocel-shop-prod-admin": "ocel-shop-prod-admin-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"admin": "b1"}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != notFoundBackend {
		t.Errorf("the tier url map routes shop.example.com onto %q, want it still on the balancer's 404: the app it was bound to has "+
			"still released nothing", got)
	}
}

func TestAHostnameBoundBeforeItsAppReleasedIsRoutedToTheLoadBalancersNotFoundBackend(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != notFoundBackend {
		t.Errorf("the tier url map routes shop.example.com onto %q, want the balancer's not-found backend %q: the bind declared no backend "+
			"for an app that has released nothing, and Compute rejects a url map naming a backend that is not there", got, notFoundBackend)
	}
}

func TestAHostnameBoundToAPromotedFunctionAppIsRoutedToItsEntryFunctionsService(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedFunctionRelease(t, stack, "web", "b1", 1, "")
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})
	bound(t, stack, "shop.example.com", "web")

	if got, want := w.hosts("ocel-alb-production-routes")["shop.example.com"], backendName("shop", environment.TierProduction, "shop.example.com"); got != want {
		t.Errorf("shop.example.com is routed to %q, want %q", got, want)
	}
	if got := hostOf(stack, "shop.example.com").Service; got != "ocel-shop-prod-web" {
		t.Errorf("the bind recorded service %q, want the entry function's ocel-shop-prod-web", got)
	}
}

func TestTheFirstReleaseOfAFunctionAppAfterABindTakesTheHostnameLive(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	bound(t, stack, "shop.example.com", "web")
	stagedFunctionRelease(t, stack, "web", "b1", 1, "")
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})

	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got == notFoundBackend {
		t.Errorf("shop.example.com is still routed to the 404 backend after its function app released")
	}
}

func openRouter(shared edge.EdgeStack) fake.PromotingStack {
	s := shared.(*stack)
	return fake.PromotingStack{Stack: routerStack{s: s}, Ledger: s.openLedger()}
}

func promotedWithHealthPath(t *testing.T, stack edge.EdgeStack) {
	t.Helper()
	ctx := context.Background()
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", Physical: "ocel-shop-prod-web", HealthPath: "/healthz",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote = %v", err)
	}
}

func TestAPromotionWarmsItsRevisionThroughTheLoadBalancerOnAHostnameRoutedToIt(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	promotedWithHealthPath(t, stack)

	want := []string{"https://shop.example.com/healthz via " + loadBalancerAddress}
	if got := w.warmedThrough(); !slices.Equal(got, want) {
		t.Errorf("the promotion warmed %v, want %v: the service's ingress admits the load balancer alone, so its run.app url turns the warm away", got, want)
	}
}

func TestAPromotionBehindAShieldedLoadBalancerWarmsThroughTheEdgeInFront(t *testing.T) {
	t.Parallel()

	_, w, bound := reconciled(t)
	if err := bound.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	bound.(*stack).recorded.LoadBalancer.Shielded = true
	promotedWithHealthPath(t, bound)

	want := []string{"https://shop.example.com/healthz via the hostname's own address"}
	if got := w.warmedThrough(); !slices.Equal(got, want) {
		t.Errorf("the promotion warmed %v, want %v: a shielded load balancer admits the edge's client certificate alone, so the warm goes where the hostname resolves", got, want)
	}
}

func TestAPromotionNoHostnameReachesWarmsNothing(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	promotedWithHealthPath(t, stack)

	if got := w.warmedThrough(); len(got) != 0 {
		t.Errorf("the promotion warmed %v, and no hostname routes to the service, so nothing could admit the request", got)
	}
}

type warnings struct {
	progress.Log
	mu   sync.Mutex
	said []string
}

func (w *warnings) Warn(message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.said = append(w.said, message)
}

func TestAWarmThatFailsThroughTheLoadBalancerNamesTheRevisionItWarmed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
	w.warmFails = errors.New("connection refused")
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{
		App: "web", Release: "b1", Physical: "ocel-shop-prod-web", HealthPath: "/healthz",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	log := &warnings{Log: progress.Discard()}

	move := router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}
	if err := openRouter(stack).MovePointer(ctx, move, log); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if len(log.said) != 1 || !strings.Contains(log.said[0], "revision ocel-shop-prod-web-00001 of ocel-shop-prod-web") {
		t.Errorf("the promotion warned %v, want one warning naming the revision it could not warm", log.said)
	}
}

func hostOf(shared edge.EdgeStack, hostname string) Host {
	return shared.(*stack).recorded.Hosts[hostname]
}
