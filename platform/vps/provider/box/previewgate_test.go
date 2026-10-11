package box_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

var recordedPreviewGate = previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func TestABoxDeclaresItGatesPreviews(t *testing.T) {
	t.Parallel()

	_, front, _ := reconciled(t)

	if !front.Facts().GatesPreviews {
		t.Error("Facts().GatesPreviews = false, want true: the switchboard on the box runs the preview gate, and a deploy of a gated preview is refused through an edge that says otherwise")
	}
}

func TestAPreviewMoveHandsTheSwitchboardThePreviewGateRecordedForItsProjectBeforeAnyHostnameIsClaimed(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.recordedPreviewGates[slug] = recordedPreviewGate
	previewed(t, previewStack(t, m), "pr-7", "web")

	surface := box.Surface(slug, environment.TierPreview)
	if got := m.previewGates[surface]; !slices.Equal(got.BypassSecrets, recordedPreviewGate.BypassSecrets) || got.Key != recordedPreviewGate.Key {
		t.Fatalf("the box holds preview gate %+v for %s, want %+v", got, surface, recordedPreviewGate)
	}
	gated, claimed := indexOf(m.calls, "preview gate "), indexOf(m.calls, "claim ")
	if gated < 0 || claimed < 0 || gated > claimed {
		t.Errorf("calls = %v, want the preview gate put before the hostname is claimed: a hostname claimed first answers ungated until the preview gate lands", m.calls)
	}
}

func TestAPreviewMoveOfAProjectWithNoRecordedPreviewGateTakesThePreviewGateTheBoxHeld(t *testing.T) {
	t.Parallel()

	m := aMachine()
	surface := box.Surface(slug, environment.TierPreview)
	m.previewGates[surface] = recordedPreviewGate
	previewed(t, previewStack(t, m), "pr-7", "web")

	if got, held := m.previewGates[surface]; held {
		t.Errorf("the box still holds preview gate %+v for %s, want it taken once none is recorded", got, surface)
	}
}

func TestAPreviewMoveWhosePreviewGateCannotBeReadClaimsNothingSoNoHostnameAnswersUngated(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.refuseOn("ReadPreviewGate", errors.New("the key-value store is unreachable"))
	stack := previewStack(t, m)
	staged(t, stack, "web", "b-pr-7", slug+"-web-pr-7")

	err := stack.MovePointer(context.Background(), router.PointerMove{Pointer: "pr-7", Hosts: listPreviewHosts("pr-7", "web"), Promotion: router.Promotion{
		PromotionID: "p-pr-7", Ts: 1, Releases: map[string]string{"web": "b-pr-7"},
	}}, progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer() = %v, want it unserved", err)
	}
	if claimed := claimedOn(t, m); len(claimed) != 0 {
		t.Errorf("the box claimed %v, want nothing", claimed)
	}
}

func TestAProductionMoveNeverReadsAPreviewGate(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.recordedPreviewGates[slug] = recordedPreviewGate
	front := edgeOver(m, fake.NewKeyValues())
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{Version: "test", Tier: environment.TierProduction, Slug: slug}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	stack := stackOn(front, stackEdge)
	staged(t, stack, "web", "b-1", slug+"-web-1")

	if err := promoted(t, stack, "p-1", "web", "b-1"); err != nil {
		t.Fatalf("promoted() = %v", err)
	}

	if m.previewGateReads != 0 || len(m.previewGates) != 0 {
		t.Errorf("a production move read %d preview gates and left %v on the box, want none", m.previewGateReads, m.previewGates)
	}
}

func TestDestroyingAPreviewStackTakesItsPreviewGateOffTheBox(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.recordedPreviewGates[slug] = recordedPreviewGate
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "web")

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	if len(m.previewGates) != 0 {
		t.Errorf("the box holds preview gates %v after the project was destroyed, want none", m.previewGates)
	}
}
