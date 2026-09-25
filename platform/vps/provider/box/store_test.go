package box_test

import (
	"context"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func storeRouted(stood *machine, slug string) {
	stood.upstream[host.RouteKey{
		Owner:   box.Surface(slug, edge.ClassProduction),
		Pointer: edge.DefaultPointer,
		App:     switchboard.StoreLabel,
	}] = "shop-prod-store-s3:9000"
}

func claimedHosts(stood *machine, app string) []string {
	held := make([]string, 0, len(stood.claims))
	for _, claim := range stood.claims {
		if claim.App == app {
			held = append(held, claim.Hostname)
		}
	}
	slices.Sort(held)
	return held
}

func TestABoundDomainPublishesTheStoreBesideTheAppThatAsksForIt(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	ctx := context.Background()
	stood := aMachine()
	storeRouted(stood, slug)
	stack := standingOn(t, stood, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if held := claimedHosts(stood, switchboard.StoreLabel); len(held) != 1 || held[0] != "storage."+hostname {
		t.Errorf("binding %s claimed %v for the store, want storage.%s: a browser handed a signed url resolves that name or nothing", hostname, held, hostname)
	}
}

func TestAProjectWithNoStoreClaimsNoNameForOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stood := aMachine()
	stack := standingOn(t, stood, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if held := claimedHosts(stood, switchboard.StoreLabel); len(held) != 0 {
		t.Errorf("a project running no store claimed %v, and a claim is a certificate this box orders for a name nothing answers", held)
	}
}

func TestAnUnboundDomainTakesTheStoresNameWithIt(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	ctx := context.Background()
	stood := aMachine()
	storeRouted(stood, slug)
	stack := standingOn(t, stood, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if err := stack.UnbindDomain(ctx, hostname); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	if held := claimedHosts(stood, switchboard.StoreLabel); len(held) != 0 {
		t.Errorf("unbinding %s left %v claimed, and this box would renew a certificate for a name it no longer serves", hostname, held)
	}
}
