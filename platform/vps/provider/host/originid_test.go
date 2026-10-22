package host

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

func TestABoxMintsItsOriginIDOnceAndHandsTheSameOneBack(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())

	first, err := box.host().Routing().EnsureOriginID(context.Background())
	if err != nil {
		t.Fatalf("EnsureOriginID() = %v", err)
	}
	again, err := box.host().Routing().EnsureOriginID(context.Background())
	if err != nil {
		t.Fatalf("EnsureOriginID() again = %v", err)
	}

	if first == "" || again != first {
		t.Errorf("EnsureOriginID() = %q then %q, want one id for the box: every Cloudflare resource this box's origin owns is named for it", first, again)
	}
	if recorded := readBack(t, box).OriginID; recorded != first {
		t.Errorf("the routing table records origin id %q, want %q", recorded, first)
	}
}

func TestABoxKeepsTheOriginIDItAlreadyHolds(t *testing.T) {
	t.Parallel()
	state := routed()
	state.OriginID = "k3j9x0"
	box := claimingBox(t, state)

	held, err := box.host().Routing().EnsureOriginID(context.Background())

	if err != nil || held != "k3j9x0" {
		t.Errorf("EnsureOriginID() = %q, %v, want the id the box already holds", held, err)
	}
}

func TestEveryBootstrapLeavesTheBoxHoldingAnOriginID(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	table := string(routingTableItem().Content)
	box := bootstrappedWith(t, tier, &table, nil)

	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	held, err := routing.ReadRoutingTable([]byte(table))
	if err != nil {
		t.Fatal(err)
	}
	if held.OriginID == "" {
		t.Errorf("the routing table after a bootstrap reads %s, want an origin id: the Cloudflare edge only reads it, and refuses a box that holds none", table)
	}
}
