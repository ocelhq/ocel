package cloudflare

import (
	"context"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

var shopGate = previewgate.Gate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

func TestAPreviewReconcileHandsTheProjectsReleasesStoreItsGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	spec := previewSpec(srv.URL, "v1")
	spec.PreviewGate = &shopGate

	if _, err := p.Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := held.heldGate(); got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("the releases store holds %+v, want the gate the deploy carried, %+v", got, shopGate)
	}
}

func TestAPreviewReconcileThatCarriesNoGateTakesDownTheOneTheStoreHolds(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	gated := previewSpec(srv.URL, "v1")
	gated.PreviewGate = &shopGate
	reconciled, err := p.Reconcile(t.Context(), gated, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(gated): %v", err)
	}

	if _, err := p.Reconcile(t.Context(), previewSpec(srv.URL, "v1"), reconciled.State()); err != nil {
		t.Fatalf("Reconcile(ungated): %v", err)
	}

	if got := held.heldGate(); got != nil {
		t.Errorf("the releases store still holds %+v after a deploy whose project is no longer protected", got)
	}
}

func TestAPrunedGlobalPreviewStackStillPublishesItsGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	spec := pruneOnlySpec(srv.URL, "v1")
	spec.GlobalPreview = "preview.app.com"
	spec.PreviewGate = &shopGate

	if _, err := p.Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := held.heldGate(); got == nil {
		t.Error("the shared preview entry reads the gate from the project's releases store, and the deploy left it none")
	}
}

func TestAProductionReconcileNeverTouchesTheGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	spec := testSpec(srv.URL, "v1")
	spec.Program.Name = "ocel-web"
	spec.Program.Worker = testStoreWorker()
	spec.PreviewGate = &shopGate

	if _, err := p.Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := held.heldGate(); got != nil {
		t.Errorf("a production stack's store holds %+v, want no gate: production is never gated", got)
	}
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	reconciled, err := p.Reconcile(t.Context(), previewSpec(srv.URL, "v1"), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	state := reconciled.State()

	edgeconformance.RunPublishedPreviewGate(t, edgeconformance.PublishedPreviewGate{
		Publish: func(ctx context.Context, gate *previewgate.Gate) error {
			return p.Hooks().PublishPreviewGate(ctx, edge.PreviewGatePublication{State: state, Gate: gate})
		},
		Read: func(context.Context) (*previewgate.Gate, error) { return held.heldGate(), nil },
	})
}
