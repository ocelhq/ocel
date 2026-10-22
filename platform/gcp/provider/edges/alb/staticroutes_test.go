package alb

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func staticRecord(build string) router.ReleaseRecord {
	return router.ReleaseRecord{
		App: "web", Framework: "next", Release: build, RootFunctionPhysical: "ocel-shop-prod-web",
		Revisions:         map[string]string{"ocel-shop-prod-web": "ocel-shop-prod-web-" + build},
		StorageCoordinate: "shop/prod/web/r000000" + build,
		Static: &edge.Static{
			ImmutablePrefixes:      []string{"/_next/static/"},
			MustRevalidatePrefixes: []string{"/_next/static/live/"},
		},
	}
}

func staticRoutesFor(build string) *StaticRoutes {
	return &StaticRoutes{
		Backend:    assetsBackend,
		Objects:    "/assets/shop/prod/web/r000000" + build + "/",
		Immutable:  []string{"/_next/static/"},
		Revalidate: []string{"/_next/static/live/"},
	}
}

func promote(t *testing.T, stack edge.EdgeStack, id, build string) {
	t.Helper()
	ctx := context.Background()
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: id, Releases: map[string]string{"web": build}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(%s) = %v", id, err)
	}
}

func stage(t *testing.T, stack edge.EdgeStack, record router.ReleaseRecord) {
	t.Helper()
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged(%s) = %v", record.Release, err)
	}
}

func bindShop(t *testing.T, stack edge.EdgeStack) {
	t.Helper()
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}
}

func expectStaticRoutes(t *testing.T, w *world, hostname string, want *StaticRoutes, why string) {
	t.Helper()
	if got := w.staticRoutesRouted(tierRoutes, hostname); !reflect.DeepEqual(got, want) {
		t.Errorf("%s is routed with static routes %+v, want %+v: %s", hostname, got, want, why)
	}
}

func TestTheReleaseThatTakesAHostnameLiveRoutesItsImmutablePrefixesToTheBackendBucket(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	bindShop(t, stack)
	stage(t, stack, staticRecord("b1"))
	promote(t, stack, "p1", "b1")

	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"), "the release that gives the hostname a service is the one whose files it serves")
}

func TestAReleaseWhoseRecordLocatesNoneOfItsObjectsIsRefusedRatherThanServedWithoutItsFiles(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	bindShop(t, stack)
	record := staticRecord("b1")
	record.StorageCoordinate = "shop/prod/web"
	stage(t, stack, record)

	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}}}, progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "locates none of its objects") {
		t.Errorf("Promote = %v, want a refusal: without its storage coordinate the hostname serves no static file", err)
	}
	expectStaticRoutes(t, w, "shop.example.com", nil, "a refused promotion routes nothing")
}

func TestAHostnameBoundAfterAReleaseRoutesThatReleasesStaticFiles(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)

	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"), "the bind reads the active release's record")
}

func TestAPromotionMovesTheStaticRoutesOfAHostnameAlreadyServedWithoutRaisingAnyStack(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	stage(t, stack, staticRecord("b2"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	raised := len(w.raised())
	promote(t, stack, "p2", "b2")

	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b2"), "a promote switches the release a hostname serves, and the objects must follow")
	if got := len(w.raised()); got != raised {
		t.Errorf("the promote raised %d more stacks, want none: only the url map's rules change", got-raised)
	}
}

func TestAReleaseOfTheSameAppOnAnotherServiceLeavesAHostnamesStaticRoutesAlone(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	other := staticRecord("b2")
	other.RootFunctionPhysical = "ocel-shop-staging-web"
	other.Revisions = map[string]string{"ocel-shop-staging-web": "ocel-shop-staging-web-b2"}
	other.StorageCoordinate = "shop/staging/web/r000000b2"
	stage(t, stack, other)
	promote(t, stack, "p2", "b2")

	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"),
		"the hostname still reaches the service it was bound to, so its immutable files must stay that service's release's")
}

func TestAPromotionBackToAnEarlierReleaseRoutesItsFilesAgain(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stage(t, stack, staticRecord("b1"))
	stage(t, stack, staticRecord("b2"))
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)
	promote(t, stack, "p2", "b2")
	promote(t, stack, "p3", "b1")

	expectStaticRoutes(t, w, "shop.example.com", staticRoutesFor("b1"), "a rollback is a promote")
}

func TestAReleaseWithNoStaticRulesRoutesNothingToTheBackendBucket(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	record := staticRecord("b1")
	record.Static = nil
	stage(t, stack, record)
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)

	expectStaticRoutes(t, w, "shop.example.com", nil, "a release with no immutable prefixes has nothing in the bucket the load balancer may serve")
}

func TestAReleaseOfAnotherFrameworkRoutesNothingToTheBackendBucket(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	record := staticRecord("b1")
	record.Framework = "node"
	stage(t, stack, record)
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)

	expectStaticRoutes(t, w, "shop.example.com", nil, "only a Next deploy uploads its static files to the tier's bucket")
}

func TestANextReleaseThatRunsAsAContainerRoutesNothingToTheBackendBucket(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	record := staticRecord("b1")
	record.RootFunctionPhysical, record.Physical = "", "ocel-shop-prod-web"
	record.Origin = "https://ocel-shop-prod-web-abc.a.run.app"
	stage(t, stack, record)
	promote(t, stack, "p1", "b1")
	bindShop(t, stack)

	expectStaticRoutes(t, w, "shop.example.com", nil, "a Next container serves the static files its image carries, and the deploy uploads none to the bucket")
}

func TestAPreviewAliasRoutesTheStaticFilesOfTheReleaseItServes(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	if err := stack.MovePointer(context.Background(), aliasMove("p1", aliasHost, staticRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	expectStaticRoutes(t, w, aliasHost, staticRoutesFor("b1"), "a preview is a release served under its own hostname")
}

func TestAnAliasMovedToANewReleaseRoutesThatReleasesFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, staticRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	if err := stack.MovePointer(ctx, aliasMove("p2", aliasHost, staticRecord("b2")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p2) = %v", err)
	}

	expectStaticRoutes(t, w, aliasHost, staticRoutesFor("b2"), "the alias follows the pointer, and the objects follow the alias")
}

func storageCoordinateOf(app, build string) string {
	return naming.Coordinate{Project: "shop", Env: "prod", App: app, Release: naming.NewReleaseToken(build, "")}.StorageCoordinate()
}
