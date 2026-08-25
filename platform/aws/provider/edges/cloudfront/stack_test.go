package cloudfront

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPromoteOntoAPointerOtherThanTheDefaultLeavesTheHostnameAlone(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	live := routeOn(t, w, stack, boundHost)
	wrote := w.store.count("kvs.UpdateKeys")

	preview := promotion()
	preview.PromotionID = "p2"
	preview.Builds = map[string]string{"web": "d2.f2"}
	if err := stack.Promote(context.Background(), preview, "pr-7"); err != nil {
		t.Fatalf("Promote onto a preview pointer: %v", err)
	}

	if got := w.store.count("kvs.UpdateKeys"); got != wrote {
		t.Errorf("UpdateKeys calls = %d, want %d: only the default pointer owns the hostnames this stack bound", got, wrote)
	}
	if after := routeOn(t, w, stack, boundHost); after.Release != live.Release {
		t.Errorf("the hostname now answers with release %q, want the production release %q: promoting a preview must not repoint production", after.Release, live.Release)
	}
}

func TestRemovePointerLeavesTheHostnameServing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		pointer string
	}{
		{"a preview pointer", "pr-7"},
		{"the default pointer", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			stack := reconciled(t, w)
			bound(t, stack)
			staged(t, stack, fakeEntryURL, fakeAssetPrefix)
			if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
				t.Fatalf("Promote: %v", err)
			}

			if _, err := stack.RemovePointer(context.Background(), tc.pointer); err != nil {
				t.Fatalf("RemovePointer: %v", err)
			}

			if published := routeOn(t, w, stack, boundHost); published.Origin != fakeEntryHost {
				t.Errorf("the hostname answers with %q, want the release it was promoted to: only unbinding the domain takes its route away", published.Origin)
			}
		})
	}
}

func TestDomainOwnerAsksTheRouteStoreBeforeListingTheAccount(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	listed := w.front.count("ListDistributions")

	owner, err := e.DomainOwner(context.Background(), strings.ToUpper(boundHost))
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}

	if owner != productionDistributionName() {
		t.Errorf("DomainOwner(%q) = %q, want the stack the route names (%q)", boundHost, owner, productionDistributionName())
	}
	if got := w.front.count("ListDistributions"); got != listed {
		t.Errorf("ListDistributions calls = %d, want %d: the route store is keyed by hostname, so nothing has to be paged through", got, listed)
	}
}

func TestASecondReconcileRereadsNothingImmutable(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	first, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := map[string]int{}
	for _, call := range []string{"ListCachePolicies", "ListResponseHeadersPolicies", "ListOriginAccessControls"} {
		before[call] = w.front.count(call)
	}

	second, err := e.Reconcile(context.Background(), testSpec(), first.State())
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	for _, call := range slices.Sorted(keysOfCount(before)) {
		if got := w.front.count(call); got != before[call] {
			t.Errorf("%s calls = %d, want %d: the stack already names an id CloudFront never changes", call, got, before[call])
		}
	}
	held, want := ownState(t, second), ownState(t, first)
	if held.CachePolicy != want.CachePolicy || held.HeadersPolicy != want.HeadersPolicy || held.OriginAccessControl != want.OriginAccessControl {
		t.Errorf("state = %+v, want the ids the first reconcile recorded (%+v)", held, want)
	}
}

func keysOfCount(held map[string]int) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range held {
			if !yield(key) {
				return
			}
		}
	}
}

func TestDestroyWaitsOutAThrottledRolloutCheck(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	w.front.statusThrottles = 2

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(w.front.distributions) != 0 {
		t.Errorf("%d distributions survived destroy, want none: a throttled status check is not a reason to leave one behind", len(w.front.distributions))
	}
}

func TestDestroyHoldsBeforeItFirstAsksHowTheRolloutIsGoing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := &provider{
		open: func(context.Context) (Clients, error) { return w.clients(), nil },
		settle: Settler{
			Wait: func(context.Context, time.Duration) error {
				w.trail.record("hold")
				return nil
			},
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		},
	}
	if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	id := ownState(t, stack).Distribution

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	steps := w.trail.taken()
	disabled := indexOf(t, steps, "UpdateDistribution "+id)
	polled := indexOf(t, steps, "GetDistribution "+id)
	held := slices.Index(steps[disabled:], "hold")
	if held < 0 || disabled+held > polled {
		t.Errorf("the calls were %v, want a hold between disabling the distribution and asking whether it settled", steps)
	}
}

func TestADistributionNamedPastTheCommentCeilingIsStillFoundByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	name := strings.Repeat("storefront-", 20)
	plan := distributionPlan{
		name:          name,
		assetOrigin:   "assets.s3.eu-west-1.amazonaws.com",
		function:      "arn:aws:cloudfront::111122223333:function/resolver",
		cachePolicy:   "cache-policy",
		headersPolicy: "headers-policy",
		oac:           "origin-access-control",
	}

	raised, err := createDistribution(context.Background(), w.clients(), plan, nil, "")
	if err != nil {
		t.Fatalf("createDistribution: %v", err)
	}
	held := w.front.distributions[raised.id]
	for field, value := range map[string]string{
		"comment":          aws.ToString(held.config.Comment),
		"caller reference": aws.ToString(held.config.CallerReference),
	} {
		if len(value) != maxDistributionNameLen {
			t.Errorf("%s is %d characters, want it clamped to the %d CloudFront accepts", field, len(value), maxDistributionNameLen)
		}
	}

	found, ok, err := findDistribution(context.Background(), w.clients(), name)
	if err != nil {
		t.Fatalf("findDistribution: %v", err)
	}
	if !ok || found.id != raised.id {
		t.Errorf("findDistribution(%q) = %+v, %v, want the distribution just raised (%q)", name, found, ok, raised.id)
	}
}

func TestBindDomainRecordsTheFrontOfADistributionFoundByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	settled, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	held := w.front.named(productionDistributionName())
	if held == nil {
		t.Fatalf("no distribution for the stack; CloudFront holds %v", w.front.mutations())
	}

	forgotten := settled.State()
	forgotten.Front = ""
	forgotten.Adapter = edge.Own(private{})
	opened, err := e.Open(forgotten)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stack := opened.(*stack)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: boundHost}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if got := stack.own.Distribution; got != held.id {
		t.Errorf("state records distribution %q, want the one already serving the stack (%q)", got, held.id)
	}
	if got := stack.State().Front; got != held.domain {
		t.Errorf("state records front %q, want %q: a binding onto a distribution found by name still has to say where DNS points", got, held.domain)
	}
}
