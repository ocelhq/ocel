package cloudflare

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var shopGate = gate.Gate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

var productionGate = gate.Gate{
	Key:           "production-gate-key",
	PasswordMAC:   "production-password-mac",
	BypassSecrets: []string{"production-bypass"},
	AllowOptions:  []string{},
}

func productionSpec(endpoint, version string) edge.StackSpec {
	spec := testSpec(endpoint, version)
	spec.Program.Name = "ocel-web"
	spec.Program.Worker = testStoreWorker()
	return spec
}

var tierSpecs = []struct {
	tier    environment.Tier
	specFor func(endpoint, version string) edge.StackSpec
}{
	{environment.TierProduction, productionSpec},
	{environment.TierPreview, previewSpec},
}

func TestAReconcileHandsTheProjectsReleasesStoreItsGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	for _, tc := range tierSpecs {
		t.Run(string(tc.tier), func(t *testing.T) {
			srv, held := fakeStoreFor(t, "")
			p := previewZoneMock().provider(t)
			spec := tc.specFor(srv.URL, "v1")
			spec.Gate = &shopGate

			if _, err := p.Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			if got := held.heldGate(); got == nil || !reflect.DeepEqual(*got, shopGate) {
				t.Errorf("the releases store holds %+v, want the gate the deploy carried, %+v", got, shopGate)
			}
		})
	}
}

func TestAReconcileThatCarriesNoGateTakesDownTheOneTheStoreHolds(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	for _, tc := range tierSpecs {
		t.Run(string(tc.tier), func(t *testing.T) {
			srv, held := fakeStoreFor(t, "")
			p := previewZoneMock().provider(t)
			gated := tc.specFor(srv.URL, "v1")
			gated.Gate = &shopGate
			reconciled, err := p.Reconcile(t.Context(), gated, edge.StackState{})
			if err != nil {
				t.Fatalf("Reconcile(gated): %v", err)
			}

			if _, err := p.Reconcile(t.Context(), tc.specFor(srv.URL, "v1"), reconciled.State()); err != nil {
				t.Fatalf("Reconcile(ungated): %v", err)
			}

			if got := held.heldGate(); got != nil {
				t.Errorf("the releases store still holds %+v after a deploy whose tier is no longer protected", got)
			}
		})
	}
}

func TestEachTierPublishesItsGateToItsOwnReleasesStore(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	productionSrv, productionHeld := fakeStoreFor(t, "")
	previewSrv, previewHeld := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	production := productionSpec(productionSrv.URL, "v1")
	production.Gate = &productionGate
	preview := previewSpec(previewSrv.URL, "v1")
	preview.Gate = &shopGate

	if _, err := p.Reconcile(t.Context(), production, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile(production): %v", err)
	}
	if _, err := p.Reconcile(t.Context(), preview, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}

	if got := productionHeld.heldGate(); got == nil || !reflect.DeepEqual(*got, productionGate) {
		t.Errorf("the production releases store holds %+v, want the production gate %+v", got, productionGate)
	}
	if got := previewHeld.heldGate(); got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("the preview releases store holds %+v, want the preview gate %+v", got, shopGate)
	}
}

func TestAnUnprotectedProductionDeployLeavesThePreviewGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	productionSrv, productionHeld := fakeStoreFor(t, "")
	previewSrv, previewHeld := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	preview := previewSpec(previewSrv.URL, "v1")
	preview.Gate = &shopGate

	if _, err := p.Reconcile(t.Context(), preview, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	if _, err := p.Reconcile(t.Context(), productionSpec(productionSrv.URL, "v1"), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile(production): %v", err)
	}

	if got := productionHeld.heldGate(); got != nil {
		t.Errorf("the production releases store holds %+v, want none: production is not protected", got)
	}
	if got := previewHeld.heldGate(); got == nil {
		t.Error("an unprotected production deploy took down the preview gate")
	}
}

func TestAPrunedGlobalPreviewStackStillPublishesItsGate(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	spec := pruneOnlySpec(srv.URL, "v1")
	spec.GlobalPreview = "preview.app.com"
	spec.Gate = &shopGate

	if _, err := p.Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := held.heldGate(); got == nil {
		t.Error("the shared preview entry reads the gate from the project's releases store, and the deploy left it none")
	}
}

func TestAnUnprotectedDeployReconcilesAgainstAReleasesStoreWithoutTheGateRoute(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	globalPreviewSpec := func(endpoint, version string) edge.StackSpec {
		spec := pruneOnlySpec(endpoint, version)
		spec.GlobalPreview = "preview.app.com"
		return spec
	}
	for _, tc := range []struct {
		name    string
		specFor func(endpoint, version string) edge.StackSpec
	}{
		{"production", productionSpec},
		{"preview", previewSpec},
		{"global preview", globalPreviewSpec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, held := fakeStoreFor(t, "")
			held.withoutGateRoute()
			p := previewZoneMock().provider(t)

			if _, err := p.Reconcile(t.Context(), tc.specFor(srv.URL, "v1"), edge.StackState{}); err != nil {
				t.Errorf("Reconcile = %v, want an unprotected deploy to need nothing of the gate route", err)
			}
		})
	}
}

func TestAProtectedDeployAgainstAReleasesStoreWithoutTheGateRouteIsRefusedNamingTheBootstrap(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	held.withoutGateRoute()
	p := previewZoneMock().provider(t)
	spec := pruneOnlySpec(srv.URL, "v1")
	spec.GlobalPreview = "preview.app.com"
	spec.Gate = &shopGate

	_, err := p.Reconcile(t.Context(), spec, edge.StackState{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "`ocel bootstrap preview`") {
		t.Errorf("Reconcile = %v, want a not-ready refusal naming `ocel bootstrap preview`", err)
	}
}

func TestPublishingAGateToAReleasesStoreWithoutTheGateRouteIsRefusedNamingTheBootstrap(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv, held := fakeStoreFor(t, "")
	p := previewZoneMock().provider(t)
	reconciled, err := p.Reconcile(t.Context(), productionSpec(srv.URL, "v1"), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	held.withoutGateRoute()

	err = p.Hooks().PublishGate(t.Context(), edge.GatePublication{State: reconciled.State(), Gate: &productionGate})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "`ocel bootstrap production`") {
		t.Errorf("PublishGate = %v, want a not-ready refusal naming `ocel bootstrap production`", err)
	}
}

func TestPublishingAGateWithoutAReleasesStoreIdentityIsRefused(t *testing.T) {
	p := previewZoneMock().provider(t)
	state := edge.StackState{Endpoint: "https://store.example", Slug: "acme-web", Tier: environment.TierProduction}

	err := p.Hooks().PublishGate(t.Context(), edge.GatePublication{State: state, Gate: &productionGate})

	if err == nil || !strings.Contains(err.Error(), "acme-web") {
		t.Errorf("PublishGate = %v, want a refusal naming the stack", err)
	}
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	for _, tc := range tierSpecs {
		t.Run(string(tc.tier), func(t *testing.T) {
			srv, held := fakeStoreFor(t, "")
			p := previewZoneMock().provider(t)
			reconciled, err := p.Reconcile(t.Context(), tc.specFor(srv.URL, "v1"), edge.StackState{})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			state := reconciled.State()

			edgeconformance.RunPublishedGate(t, edgeconformance.PublishedGate{
				Publish: func(ctx context.Context, published *gate.Gate) error {
					return p.Hooks().PublishGate(ctx, edge.GatePublication{State: state, Gate: published})
				},
				Read: func(context.Context) (*gate.Gate, error) { return held.heldGate(), nil },
			})
		})
	}
}
