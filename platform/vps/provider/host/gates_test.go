package host

import (
	"context"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

var shopGate = gate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"b"}, AllowOptions: []string{"/api"}}

func TestTheRoutingTableReadsBackTheGatesItHolds(t *testing.T) {
	t.Parallel()

	table := RoutingTable{Grace: DrainWindow, Gates: []OwnedGate{{Owner: surface, Gate: gate.Gate{Key: "j"}}, {Owner: otherSurface, Gate: shopGate}}}

	read, err := ReadRoutingTable(mustWrite(t, table))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if len(read.Gates) != 2 || read.Gates[0].Owner != otherSurface || read.Gates[1].Owner != surface {
		t.Fatalf("the table reads back gates %+v, want both, sorted by owner so the same gates are the same bytes", read.Gates)
	}
	if !reflect.DeepEqual(read.Gates[0].Gate.BypassSecrets, shopGate.BypassSecrets) || read.Gates[0].Gate.PasswordMAC != "mac" {
		t.Errorf("the gate reads back as %+v, want %+v", read.Gates[0].Gate, shopGate)
	}
}

func TestPuttingAGateRecordsItForItsOwnerAndReplacesTheOneItHeld(t *testing.T) {
	t.Parallel()

	box := claimingBox(t, routed())
	h := box.host()
	for _, gate := range []gate.Gate{{Key: "old"}, shopGate} {
		if err := h.PutGate(context.Background(), surface, &gate); err != nil {
			t.Fatalf("PutGate() = %v", err)
		}
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Gates) != 1 || table.Gates[0].Owner != surface || table.Gates[0].Gate.Key != "k" {
		t.Errorf("the table holds gates %+v, want only the latest for %s", table.Gates, surface)
	}
}

func TestPuttingNoGateTakesTheOneTheOwnerHeldAndNoOnesElse(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Gates = []OwnedGate{{Owner: surface, Gate: shopGate}, {Owner: otherSurface, Gate: shopGate}}
	box := claimingBox(t, state)
	if err := box.host().PutGate(context.Background(), surface, nil); err != nil {
		t.Fatalf("PutGate() = %v", err)
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Gates) != 1 || table.Gates[0].Owner != otherSurface {
		t.Errorf("the table holds gates %+v, want only %s's", table.Gates, otherSurface)
	}
}

func TestEachTierOfAProjectHoldsItsOwnGateAndTakingOneLeavesTheOther(t *testing.T) {
	t.Parallel()

	preview := live.Surface("shop", string(environment.TierPreview))
	box := claimingBox(t, routed())
	h := box.host()
	previewGate := gate.Gate{Key: "preview"}
	for owner, held := range map[string]*gate.Gate{surface: &shopGate, preview: &previewGate} {
		if err := h.PutGate(context.Background(), owner, held); err != nil {
			t.Fatalf("PutGate(%s) = %v", owner, err)
		}
	}
	if err := h.PutGate(context.Background(), preview, nil); err != nil {
		t.Fatalf("PutGate(%s, nil) = %v", preview, err)
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Gates) != 1 || table.Gates[0].Owner != surface || table.Gates[0].Gate.Key != shopGate.Key {
		t.Errorf("the table holds gates %+v, want only the production one once the preview one is taken", table.Gates)
	}
}

func TestDisclaimingASurfaceTakesItsGateWithIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Gates = []OwnedGate{{Owner: surface, Gate: shopGate}, {Owner: otherSurface, Gate: shopGate}}
	box := claimingBox(t, state)
	if err := box.host().DisclaimSurface(context.Background(), surface); err != nil {
		t.Fatalf("DisclaimSurface() = %v", err)
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Gates) != 1 || table.Gates[0].Owner != otherSurface {
		t.Errorf("the table holds gates %+v, want a torn-down surface's keys gone", table.Gates)
	}
}

func TestAGateWithNoKeyIsRefusedBeforeTheBoxIsTouched(t *testing.T) {
	t.Parallel()

	box := claimingBox(t, routed())
	before := box.recorded

	if err := box.host().PutGate(context.Background(), surface, &gate.Gate{}); err == nil {
		t.Fatal("PutGate() accepted a gate with no key")
	}
	if box.recorded != before {
		t.Errorf("the table changed to\n%s\nwant it untouched", box.recorded)
	}
}
