package host

import (
	"context"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

var shopPreviewGate = previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"b"}, AllowOptions: []string{"/api"}}

func TestTheRoutingTableReadsBackThePreviewGatesItHolds(t *testing.T) {
	t.Parallel()

	table := RoutingTable{Grace: DrainWindow, PreviewGates: []PreviewGate{{Owner: surface, PreviewGate: previewgate.Gate{Key: "j"}}, {Owner: otherSurface, PreviewGate: shopPreviewGate}}}

	read, err := ReadRoutingTable(mustWrite(t, table))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if len(read.PreviewGates) != 2 || read.PreviewGates[0].Owner != otherSurface || read.PreviewGates[1].Owner != surface {
		t.Fatalf("the table reads back preview gates %+v, want both, sorted by owner so the same preview gates are the same bytes", read.PreviewGates)
	}
	if !reflect.DeepEqual(read.PreviewGates[0].PreviewGate.BypassSecrets, shopPreviewGate.BypassSecrets) || read.PreviewGates[0].PreviewGate.PasswordMAC != "mac" {
		t.Errorf("the preview gate reads back as %+v, want %+v", read.PreviewGates[0].PreviewGate, shopPreviewGate)
	}
}

func TestPuttingAPreviewGateRecordsItForItsOwnerAndReplacesTheOneItHeld(t *testing.T) {
	t.Parallel()

	box := claimingBox(t, routed())
	h := box.host()
	for _, previewGate := range []previewgate.Gate{{Key: "old"}, shopPreviewGate} {
		if err := h.PutPreviewGate(context.Background(), surface, &previewGate); err != nil {
			t.Fatalf("PutPreviewGate() = %v", err)
		}
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.PreviewGates) != 1 || table.PreviewGates[0].Owner != surface || table.PreviewGates[0].PreviewGate.Key != "k" {
		t.Errorf("the table holds preview gates %+v, want only the latest for %s", table.PreviewGates, surface)
	}
}

func TestPuttingNoPreviewGateTakesTheOneTheOwnerHeldAndNoOnesElse(t *testing.T) {
	t.Parallel()

	state := routed()
	state.PreviewGates = []PreviewGate{{Owner: surface, PreviewGate: shopPreviewGate}, {Owner: otherSurface, PreviewGate: shopPreviewGate}}
	box := claimingBox(t, state)
	if err := box.host().PutPreviewGate(context.Background(), surface, nil); err != nil {
		t.Fatalf("PutPreviewGate() = %v", err)
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.PreviewGates) != 1 || table.PreviewGates[0].Owner != otherSurface {
		t.Errorf("the table holds preview gates %+v, want only %s's", table.PreviewGates, otherSurface)
	}
}

func TestDisclaimingASurfaceTakesItsPreviewGateWithIt(t *testing.T) {
	t.Parallel()

	state := routed()
	state.PreviewGates = []PreviewGate{{Owner: surface, PreviewGate: shopPreviewGate}, {Owner: otherSurface, PreviewGate: shopPreviewGate}}
	box := claimingBox(t, state)
	if err := box.host().DisclaimSurface(context.Background(), surface); err != nil {
		t.Fatalf("DisclaimSurface() = %v", err)
	}

	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.PreviewGates) != 1 || table.PreviewGates[0].Owner != otherSurface {
		t.Errorf("the table holds preview gates %+v, want a torn-down surface's keys gone", table.PreviewGates)
	}
}

func TestAPreviewGateWithNoKeyIsRefusedBeforeTheBoxIsTouched(t *testing.T) {
	t.Parallel()

	box := claimingBox(t, routed())
	before := box.recorded

	if err := box.host().PutPreviewGate(context.Background(), surface, &previewgate.Gate{}); err == nil {
		t.Fatal("PutPreviewGate() accepted a preview gate with no key")
	}
	if box.recorded != before {
		t.Errorf("the table changed to\n%s\nwant it untouched", box.recorded)
	}
}
