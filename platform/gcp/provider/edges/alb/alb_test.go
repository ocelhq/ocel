package alb

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
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
			return front, edge.StackSpec{Slug: "shop", Class: edge.ClassProduction}
		},
		Hostname: "shop.example.com",
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			front, _ := fronting(t)
			return front, edge.StackSpec{Slug: "shop", Class: edge.ClassPreview, PruneOnly: true}, previewWildcard()
		},
	})
}

func reconciled(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	front, w := fronting(t)
	stack, err := front.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Class: edge.ClassProduction}, edge.StackState{})
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

	want := BindingStack("shop", edge.ClassProduction)
	if got := w.raised(); !slices.Contains(got, want) {
		t.Errorf("the bind raised %v, want %q among them: a hostname's certificate, neg and backend are one stack per project and class", got, want)
	}
	routed := w.hosts("ocel-alb-production-routes")
	if backend, found := routed["shop.example.com"]; !found || backend == "" {
		t.Errorf("the class url map routes %v, want shop.example.com onto the backend the bind provisioned", routed)
	}
	if front := stack.State().Fronts["shop.example.com"]; front != frontAddress {
		t.Errorf("the bind published %q as the front, want the load balancer's address %q for DNS to point at", front, frontAddress)
	}
}

func TestTwoProjectsClaimingOneHostnameAtOnceLeaveItWithExactlyOne(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	ctx := context.Background()
	stacks := map[string]edge.EdgeStack{}
	for _, slug := range []string{"shop", "store"} {
		stack, err := front.Reconcile(ctx, edge.StackSpec{Slug: slug, Class: edge.ClassProduction}, edge.StackState{})
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

	owner, err := front.DomainOwner(ctx, "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for slug, stack := range stacks {
		bound := slices.Contains(stack.State().Bound, "shop.example.com")
		if owns := owner == Surface(slug, edge.ClassProduction); owns != bound {
			t.Errorf("%s reads as bound=%t while the claim names %q: what the ledger says and what the url map routes must be one project", slug, bound, owner)
		}
	}
	if _, routed := w.hosts("ocel-alb-production-routes")["shop.example.com"]; !routed {
		t.Error("the url map has no rule for shop.example.com after the winning bind")
	}
}

func TestAHostnameAnotherProjectServesIsRefusedRatherThanTakenOver(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	ctx := context.Background()
	shop, err := front.Reconcile(ctx, edge.StackSpec{Slug: "shop", Class: edge.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shop.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain(shop) = %v", err)
	}
	store, err := front.Reconcile(ctx, edge.StackSpec{Slug: "store", Class: edge.ClassProduction}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	before := w.hosts("ocel-alb-production-routes")
	raised := len(w.raised())

	err = store.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, Surface("shop", edge.ClassProduction)) {
		t.Fatalf("BindDomain(store) = %v, want a refusal naming the project that serves the hostname", err)
	}
	if after := w.hosts("ocel-alb-production-routes"); !maps.Equal(before, after) {
		t.Errorf("the refused bind rewrote the url map from %v to %v, and a refusal that rerouted is a takeover", before, after)
	}
	if len(w.raised()) != raised {
		t.Errorf("the refused bind raised %v, and a stack for a hostname another project serves is one nothing routes to", w.raised()[raised:])
	}
	if owner, _ := front.DomainOwner(ctx, "shop.example.com"); owner != Surface("shop", edge.ClassProduction) {
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

	want := BindingStack("shop", edge.ClassProduction)
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
			App: "web", Build: build.identity, Physical: "ocel-shop-prod-web",
			Revisions: map[string]string{"ocel-shop-prod-web": build.revision},
		}); err != nil {
			t.Fatalf("PutStaged(%s) = %v", build.identity, err)
		}
	}
	for _, step := range []struct{ id, identity string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		err := stack.Promote(ctx, edge.Promotion{PromotionID: step.id, Builds: map[string]string{"web": step.identity}},
			"", edge.DiscardProgress())
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
	delete(w.outputs, FrontStack(edge.ClassProduction))

	var refused refusal.Refusal
	_, err := front.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Class: edge.ClassProduction}, edge.StackState{})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Reconcile with no front provisioned = %v, want a %s refusal", err, refusal.CodeNotReady)
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

func TestOneHostRuleAndOneMaskedNegAnswerEveryPreviewHostname(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	published, err := front.ReconcilePreviewWildcard(context.Background(), previewWildcard())
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if published != frontAddress {
		t.Errorf("ReconcilePreviewWildcard published %q, want the class front's address %q for DNS to point the wildcard at", published, frontAddress)
	}

	routed := w.hosts("ocel-alb-production-routes")
	backend := routed[edge.PreviewWildcard(previewBase)]
	if backend == "" {
		t.Fatalf("the class url map routes %v, want one host rule for %s: every preview resolves through it and none writes its own",
			routed, edge.PreviewWildcard(previewBase))
	}
	resources := w.declarations(FrontStack(edge.ClassPreview))
	neg, declared := resources[previewNEGName(previewBase)]
	if !declared {
		t.Fatalf("the preview front declares %v, want a serverless network endpoint group the host rule's backend reaches Cloud Run through", keys(resources))
	}
	if mask := cloudRunMask(neg); mask != "<service>."+previewBase {
		t.Errorf("the neg has the url mask %q, want %q: the mask is what turns a hostname's label into the Cloud Run service that answers it",
			mask, "<service>."+previewBase)
	}
	if _, present := resources[backend]; !present {
		t.Errorf("the host rule points at the backend %q, and the front declares only %v", backend, keys(resources))
	}
	if !cachesByHost(resources[backend]) {
		t.Errorf("the preview backend declares the cache key policy %v, want the host in it: one backend answers every preview "+
			"hostname on the wildcard, so a key that leaves the host out serves one preview's bytes to another",
			cacheKeyPolicy(resources[backend]))
	}
	if _, entered := resources[previewEntryName(previewBase)]; !entered {
		t.Errorf("the preview front declares %v, want a certificate map entry: nothing terminates TLS for %s without one",
			keys(resources), edge.PreviewWildcard(previewBase))
	}
}

func TestTheWildcardIsOwnedByTheSharedPreviewEntryRatherThanByAProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, _ := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}

	owner, err := front.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil {
		t.Fatalf("DomainOwner = %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, want %q: the wildcard is the bootstrap's, and a project that asks whether it may bind it "+
			"has to be told it is already served", edge.PreviewWildcard(previewBase), owner, edge.PreviewEntryOwner)
	}
	if owner, err := front.DomainOwner(ctx, edge.PreviewWildcard("other.example.com")); err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing: no wildcard but the one raised is served", edge.PreviewWildcard("other.example.com"), owner, err)
	}
}

func TestAPreviewWildcardWithNoCertificateIsRefusedRatherThanServedOnPlainHttp(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	var refused refusal.Refusal
	_, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: previewBase})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("ReconcilePreviewWildcard with no certificate = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}

func TestRaisingTheClassFrontAgainLeavesTheWildcardRouting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, w := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if _, err := front.Bootstrap(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}

	resources := w.declarations(FrontStack(edge.ClassPreview))
	if _, declared := resources[previewNEGName(previewBase)]; !declared {
		t.Errorf("the front raised again declares %v, and the wildcard's neg is gone from it: a bootstrap that reruns would "+
			"take every preview in the class down", keys(resources))
	}
}

func TestDestroyingThePreviewWildcardTakesItsRouteAndLeavesTheFrontInPlace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, w := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	if err := front.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard = %v", err)
	}

	if routed := w.hosts("ocel-alb-production-routes"); routed[edge.PreviewWildcard(previewBase)] != "" {
		t.Errorf("the class url map still routes %v after the wildcard was released", routed)
	}
	resources := w.declarations(FrontStack(edge.ClassPreview))
	if _, declared := resources[previewNEGName(previewBase)]; declared {
		t.Errorf("the front still declares %v after the wildcard was released: bytes a release leaves behind must be zero", keys(resources))
	}
	if slices.Contains(w.torn(), FrontStack(edge.ClassPreview)) {
		t.Error("releasing the wildcard destroyed the class front, which every project in the class is answered by")
	}
	owner, err := front.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing serving a released wildcard", edge.PreviewWildcard(previewBase), owner, err)
	}
	if err := front.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Errorf("DestroyPreviewWildcard again = %v, want a release to be re-entrant", err)
	}
}

func TestAWildcardWhoseFrontFailsToRiseIsOwnedByNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, w := fronting(t)
	w.breakUp(FrontStack(edge.ClassPreview), errors.New("the preview front would not rise"))

	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err == nil {
		t.Fatal("ReconcilePreviewWildcard = nil, want the failure the front reported")
	}

	owner, err := front.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing: a reconcile that never raised a front serves no wildcard, "+
			"and a record claiming it would refuse every project that asks to bind one", edge.PreviewWildcard(previewBase), owner, err)
	}
}

func TestAWildcardWhoseTeardownFailsIsStillOwnedSoTheRetryStillTearsItDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, w := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	w.breakUp(FrontStack(edge.ClassPreview), errors.New("the preview front would not rise"))

	if err := front.DestroyPreviewWildcard(ctx, previewBase); err == nil {
		t.Fatal("DestroyPreviewWildcard = nil, want the failure the front reported")
	}

	owner, err := front.DomainOwner(ctx, edge.PreviewWildcard(previewBase))
	if err != nil || owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, %v, want %q: the neg and the certificate map entry are still provisioned, "+
			"and a retry that read the wildcard as gone would leave them there forever",
			edge.PreviewWildcard(previewBase), owner, err, edge.PreviewEntryOwner)
	}
}

func TestThePreviewWildcardIsKeptWhileAProjectIsStillServedOnIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, _ := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	servedOnPreview(t, front, "shop", previewBase)

	var refused refusal.Refusal
	err := front.DestroyPreviewWildcard(ctx, previewBase)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("DestroyPreviewWildcard with a project still served = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "shop") {
		t.Errorf("the refusal reads %q, want the projects served on it named: they are what the operator has to release", refused.Message)
	}
}

func TestAPromotionOfAPreviewOnTheGlobalWildcardWritesNoHostRule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	front, w := fronting(t)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewWildcard()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard = %v", err)
	}
	stack, err := front.Reconcile(ctx,
		edge.StackSpec{Slug: "shop", Class: edge.ClassPreview, PruneOnly: true},
		edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile = %v", err)
	}
	if !stack.State().ServedOnGlobalPreview(previewBase) {
		t.Fatalf("state = %+v, want the stack to record the wildcard it is served on", stack.State())
	}
	before := w.hosts("ocel-alb-production-routes")

	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App: "web", Build: "b1", Physical: "shop--pr-7",
		Revisions: map[string]string{"shop--pr-7": "shop--pr-7-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}},
		"pr-7", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	if after := w.hosts("ocel-alb-production-routes"); !maps.Equal(after, before) {
		t.Errorf("the promotion left the class url map routing %v, want the %v it found: a preview on the shared wildcard "+
			"resolves through the one host rule, and a write per preview is what this design exists to avoid", after, before)
	}
}

func TestAProjectsOwnPreviewWildcardIsRefusedOnTheLoadBalancer(t *testing.T) {
	t.Parallel()

	_, _, stack := reconciledPreview(t)
	var refused refusal.Refusal
	err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: edge.PreviewWildcard("preview.shop.example"), App: "web", Certificate: previewCertificate,
	})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("BindDomain of a project's own preview wildcard = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "domains.preview") {
		t.Errorf("the refusal reads %q, want what the project declared named: it is what has to be removed", refused.Message)
	}
}

func TestTheWildcardRemovalPlanNamesWhatComesDownAndWhatStays(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	removed, kept := front.PreviewWildcardRemovals(edge.PreviewWildcard(previewBase))
	named := map[string]bool{}
	for _, change := range removed.Changes {
		named[change.Kind] = true
	}
	for _, kind := range []string{
		"compute.URLMap host rule",
		"compute.BackendService",
		"compute.RegionNetworkEndpointGroup",
		"certificatemanager.CertificateMapEntry",
	} {
		if !named[kind] {
			t.Errorf("the removal plan names %v, want a %s row: a plan that hides a resource leaves it provisioned and billing", removed.Changes, kind)
		}
	}
	if kept.Action != edge.PlanKeep || kept.Reason == "" {
		t.Errorf("the kept group = %+v, want the front kept with a reason", kept)
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

func cacheKeyPolicy(backend declaration) map[string]any {
	policy, ok := backend.Args["cdnPolicy"].(map[string]any)
	if !ok {
		return nil
	}
	key, _ := policy["cacheKeyPolicy"].(map[string]any)
	return key
}

func cachesByHost(backend declaration) bool {
	host, _ := cacheKeyPolicy(backend)["includeHost"].(bool)
	return host
}

func reconciledPreview(t *testing.T) (*Edge, *world, edge.EdgeStack) {
	t.Helper()
	front, w := fronting(t)
	stack, err := front.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Class: edge.ClassPreview}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return front, w, stack
}

func servedOnPreview(t *testing.T, front *Edge, slug, base string) {
	t.Helper()
	ctx := context.Background()
	stack, err := front.Reconcile(ctx,
		edge.StackSpec{Slug: slug, Class: edge.ClassPreview, PruneOnly: true},
		edge.StackState{GlobalPreview: base})
	if err != nil {
		t.Fatalf("Reconcile(%s) = %v", slug, err)
	}
	state := stackrecords.EdgeState{Kind: Kind, Edge: stack.State()}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	name := stackrecords.EdgeStackRecord(edge.ClassPreview, slug)
	record, err := records.ReadOrEmpty(ctx, front.deps.Records, name)
	if err != nil {
		t.Fatal(err)
	}
	record.Bytes = encoded
	if _, err := front.deps.Records.Write(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func TestARemovalPlanNamesTheRecurringCostItLeavesBehind(t *testing.T) {
	t.Parallel()

	front, _ := fronting(t)
	groups := front.ProjectRemovals(edge.ProjectScope{
		Slug: "shop", Class: edge.ClassProduction, Hostnames: []string{"shop.example.com"},
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
		t.Errorf("the kept group reads %q, want the recurring cost named: the load balancer outlives the project it fronted", kept.Reason)
	}
}

func TestTheFrontIsNotTakenDownWhileAHostnameIsStillEnteredInItsCertificateMap(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	w.enter("ocel-alb-production-certs", "shop.example.com")

	var refused refusal.Refusal
	err := front.Teardown(context.Background(), edge.ClassProduction)
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

func TestTheFrontComesDownOnceNothingIsBoundToIt(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	if err := front.Teardown(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Teardown = %v", err)
	}
	if want := FrontStack(edge.ClassProduction); !slices.Contains(w.torn(), want) {
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
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App: "web", Build: "b1", Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}},
		"", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	want := backendName("shop", edge.ClassProduction, "shop.example.com")
	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != want {
		t.Errorf("the class url map routes shop.example.com onto %q, want the project's own backend %q: the bind served the hostname a 404 "+
			"because the app had released nothing, and the release that gives it a service is what takes it live", got, want)
	}
}

func TestAHostnameBoundAfterAReleaseIsRoutedToThePromotedService(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, w, stack := reconciled(t)
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App: "web", Build: "b1", Physical: "ocel-shop-prod-web",
		Revisions: map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}},
		"", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote = %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	want := backendName("shop", edge.ClassProduction, "shop.example.com")
	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != want {
		t.Errorf("the class url map routes shop.example.com onto %q, want the project's own backend %q: the release was promoted before the bind, "+
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
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App: "admin", Build: "b1", Physical: "ocel-shop-prod-admin",
		Revisions: map[string]string{"ocel-shop-prod-admin": "ocel-shop-prod-admin-00001"},
	}); err != nil {
		t.Fatalf("PutStaged = %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Builds: map[string]string{"admin": "b1"}},
		"", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote = %v", err)
	}

	if got := w.hosts("ocel-alb-production-routes")["shop.example.com"]; got != notFoundBackend {
		t.Errorf("the class url map routes shop.example.com onto %q, want it still on the front's 404: the app it was bound to has "+
			"still released nothing", got)
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
