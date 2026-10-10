package cloudfront

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func promotedWith(t *testing.T, w *world, e *cloudFront, mutate func(*router.ReleaseRecord)) route {
	t.Helper()
	if _, err := e.Bootstrap(context.Background(), testSpec().Tier); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	record := router.ReleaseRecord{
		App:                  "web",
		Release:              "d1.f1",
		RootFunction:         "/",
		RootFunctionPhysical: entryFunction,
		FunctionURLs:         map[string]string{"/": fakeEntryURL},
		AssetPrefix:          fakeAssetPrefix,
	}
	mutate(&record)
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion()}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	bound(t, stack)
	return routeOn(t, w, stack, boundHost)
}

func TestAReleaseWithPrerendersIsShieldedInTheRegionOfItsFunction(t *testing.T) {
	t.Parallel()

	w := newWorld()
	published := promotedWith(t, w, w.edge(), func(r *router.ReleaseRecord) { r.Prerenders = true })

	if published.ShieldRegion != fakeRegion {
		t.Errorf("shieldRegion = %q, want %q: requests from the function's own region bypass the shield, so any other region would add a hop", published.ShieldRegion, fakeRegion)
	}
}

func TestAReleaseWithNoPrerendersIsNotShielded(t *testing.T) {
	t.Parallel()

	w := newWorld()
	published := promotedWith(t, w, w.edge(), func(*router.ReleaseRecord) {})

	if published.ShieldRegion != "" {
		t.Errorf("shieldRegion = %q, want none: with nothing prerendered the shield only adds a charge per request", published.ShieldRegion)
	}
}

func TestTurningTheOriginShieldOffLeavesEveryReleaseUnshielded(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := w.edge()
	e.options = Options{OriginShield: ptr(false)}
	published := promotedWith(t, w, e, func(r *router.ReleaseRecord) { r.Prerenders = true })

	if published.ShieldRegion != "" {
		t.Errorf("shieldRegion = %q, want none: the project turned the shield off", published.ShieldRegion)
	}
}

func TestAContainerReleaseIsNeverShielded(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := w.edge()
	recordFront(t, w, testSpec().Tier)
	published := promotedWith(t, w, e, func(r *router.ReleaseRecord) {
		r.Prerenders = true
		r.Origin = "http://" + fakeFront.Host
		r.Physical = "shop-prod-web-container-r3f8a1c90"
	})

	if published.ShieldRegion != "" {
		t.Errorf("shieldRegion = %q, want none: a container is reached over a VPC origin, which the function cannot update", published.ShieldRegion)
	}
}

func TestOriginShieldRegionIsWhereCloudFrontOffersItNearestTheFunction(t *testing.T) {
	t.Parallel()

	for region, want := range map[string]string{
		"eu-west-1":      "eu-west-1",
		"us-east-1":      "us-east-1",
		"me-central-1":   "me-central-1",
		"us-west-1":      "us-west-2",
		"ca-central-1":   "us-east-1",
		"af-south-1":     "eu-west-1",
		"eu-north-1":     "eu-west-2",
		"me-south-1":     "ap-south-1",
		"ap-northeast-3": "ap-northeast-1",
		"ap-east-2":      "ap-northeast-1",
		"ap-southeast-3": "ap-southeast-1",
		"ap-southeast-4": "ap-southeast-2",
		"ap-southeast-5": "ap-southeast-1",
		"ap-southeast-6": "ap-southeast-2",
		"ap-southeast-7": "ap-southeast-1",
		"ap-south-2":     "ap-south-1",
		"eu-central-2":   "eu-central-1",
		"eu-south-2":     "eu-west-3",
		"il-central-1":   "eu-central-1",
		"ca-west-1":      "us-west-2",
		"mx-central-1":   "us-east-2",
		"nowhere-mid-1":  "",
		"":               "",
	} {
		if got := originShieldRegionFor(region); got != want {
			t.Errorf("originShieldRegionFor(%q) = %q, want %q", region, got, want)
		}
	}
}
