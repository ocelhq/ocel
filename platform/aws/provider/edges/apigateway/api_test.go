package apigateway

import (
	"context"
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestEveryRestAPICarriesTheTagsOfItsNamespaceProjectTierAndPreview(t *testing.T) {
	t.Parallel()

	production := newWorld()
	reconciled(t, production)
	previews := newWorld()
	_, stack := previewing(t, previews)
	promotePreview(t, stack, previewPoint)

	for _, tc := range []struct {
		w    *world
		name string
		want map[string]string
	}{
		{production, productionAPIName(), map[string]string{
			"ocel:managed-by": "ocel",
			"ocel:namespace":  "ocel",
			"ocel:project":    conformanceSlug,
			"ocel:env-tier":   "production",
		}},
		{previews, previewAPIName, map[string]string{
			"ocel:managed-by": "ocel",
			"ocel:namespace":  "ocel",
			"ocel:project":    conformanceSlug,
			"ocel:env-tier":   "preview",
			"ocel:env":        previewPoint,
		}},
	} {
		api := tc.w.gateway.named(tc.name)
		if api == nil {
			t.Fatalf("no REST API named %s was created", tc.name)
		}
		if !maps.Equal(api.tags, tc.want) {
			t.Errorf("%s is tagged %v, want %v", tc.name, api.tags, tc.want)
		}
	}
}

func TestDomainOwnerListsAsMuchHoweverManyRestAPIsTheAccountHolds(t *testing.T) {
	t.Parallel()

	ownerAmong := func(others int) int {
		ctx := context.Background()
		w := newWorld()
		e := bootstrapped(t, w)
		stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		seedCustomerAPIs(t, w, others)
		w.gateway.calls = nil

		owner, err := e.DomainOwner(ctx, "shop.example.com")
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != productionAPIName() {
			t.Errorf("DomainOwner = %q, want %q", owner, productionAPIName())
		}
		return countListings(w)
	}

	alone, among := ownerAmong(0), ownerAmong(20)
	if alone != among {
		t.Errorf("DomainOwner lists %d times beside no other REST APIs and %d times beside 20 of them; it reads the one REST API the host is mapped to", alone, among)
	}
}

func TestAPreviewPointerMoveAndRemovalListAsMuchHoweverManyRestAPIsOtherProjectsHold(t *testing.T) {
	t.Parallel()

	moveAndRemoveAmong := func(others int) int {
		ctx := context.Background()
		w := newWorld()
		_, stack := previewing(t, w)
		promotePreview(t, stack, previewPoint)
		seedOtherProjectsAPIs(t, w, environment.TierPreview, others)
		w.gateway.calls = nil

		again := router.Promotion{PromotionID: "p-again", Ts: 2, Releases: map[string]string{"web": "d1.f1"}}
		if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Pointer: previewPoint, Hosts: listPreviewHosts(previewPoint), Promotion: again}, progress.Discard()); err != nil {
			t.Fatalf("MovePointer: %v", err)
		}
		if created := w.gateway.count("CreateRestApi"); created != 0 {
			t.Errorf("moving %s again created %d REST APIs; it must find the one it already serves", previewPoint, created)
		}
		if _, err := removePointer(ctx, stack, router.PointerRemoval{Pointer: previewPoint, Hosts: listPreviewHosts(previewPoint)}, progress.Discard()); err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		if w.gateway.named(previewAPIName) != nil {
			t.Errorf("the REST API for %s survived the removal of its pointer", previewPoint)
		}
		return countListings(w)
	}

	alone, among := moveAndRemoveAmong(0), moveAndRemoveAmong(20)
	if alone != among {
		t.Errorf("a pointer move and removal list %d times beside no other project's REST APIs and %d times beside 20 of them; their listing must be bounded by their own project", alone, among)
	}
}
