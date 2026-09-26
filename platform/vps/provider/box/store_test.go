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

func storeRouted(m *machine, slug string) {
	m.upstream[host.RouteKey{
		Owner:   box.Surface(slug, edge.ClassProduction),
		Pointer: edge.DefaultPointer,
		App:     switchboard.StoreLabel,
	}] = "shop-prod-store-s3:9000"
}

func claimedHosts(m *machine, app string) []string {
	claimed := make([]string, 0, len(m.claims))
	for _, claim := range m.claims {
		if claim.App == app {
			claimed = append(claimed, claim.Hostname)
		}
	}
	slices.Sort(claimed)
	return claimed
}

func TestABoundDomainPublishesTheStoreBesideTheAppThatAsksForIt(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	ctx := context.Background()
	m := aMachine()
	storeRouted(m, slug)
	stack := reconciledOn(t, m, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if claimed := claimedHosts(m, switchboard.StoreLabel); len(claimed) != 1 || claimed[0] != "storage."+hostname {
		t.Errorf("binding %s claimed %v for the store, want storage.%s: a browser handed a signed url resolves that name or nothing", hostname, claimed, hostname)
	}
}

func TestAProjectWithNoStoreClaimsNoNameForOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := aMachine()
	stack := reconciledOn(t, m, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if claimed := claimedHosts(m, switchboard.StoreLabel); len(claimed) != 0 {
		t.Errorf("a project running no store claimed %v, and a claim is a certificate this box orders for a name nothing answers", claimed)
	}
}

func TestAnUnboundDomainTakesTheStoresNameWithIt(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	ctx := context.Background()
	m := aMachine()
	storeRouted(m, slug)
	stack := reconciledOn(t, m, slug)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if err := stack.UnbindDomain(ctx, hostname); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	if claimed := claimedHosts(m, switchboard.StoreLabel); len(claimed) != 0 {
		t.Errorf("unbinding %s left %v claimed, and this box would renew a certificate for a name it no longer serves", hostname, claimed)
	}
}
