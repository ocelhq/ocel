package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestFinalizeProductionDeploy_ReconcileThenStageThenPromoteInOrder(t *testing.T) {
	fake := &recordingRootTier{}
	ctx := context.Background()
	specs := []edge.RootTierSpec{{Version: "v1", GenericName: "web-generic"}}
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", BuildID: "b2", Record: edge.DeploymentRecord{App: "api", BuildID: "b2"}},
	}

	state, err := finalizeProductionDeploy(ctx, fake, specs, nil, "promo1", 100, results)
	if err != nil {
		t.Fatalf("finalizeProductionDeploy: %v", err)
	}

	if len(fake.reconciles) != 1 {
		t.Fatalf("reconciles = %d, want 1", len(fake.reconciles))
	}
	if len(fake.staged) != 2 {
		t.Fatalf("staged = %d, want 2 (one per app)", len(fake.staged))
	}
	if len(fake.promotions) != 1 {
		t.Fatalf("promotions = %d, want 1", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" {
		t.Errorf("promotion id = %q, want %q", fake.promotions[0].PromotionID, "promo1")
	}
	want := map[string]string{"web": "b1", "api": "b2"}
	if len(fake.promotions[0].Builds) != len(want) {
		t.Fatalf("promotion builds = %v, want %v", fake.promotions[0].Builds, want)
	}
	for app, buildID := range want {
		if got := fake.promotions[0].Builds[app]; got != buildID {
			t.Errorf("promotion.Builds[%q] = %q, want %q", app, got, buildID)
		}
	}
	if state[edge.RootTierKeyEndpoint] == "" {
		t.Error("expected a reconciled state to be returned")
	}
}

func TestFinalizeProductionDeploy_StagesBeforeAnyPromote(t *testing.T) {
	fake := &orderTrackingRootTier{recordingRootTier: &recordingRootTier{}}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	if _, err := finalizeProductionDeploy(ctx, fake, []edge.RootTierSpec{{Version: "v1"}}, nil, "promo1", 100, results); err != nil {
		t.Fatalf("finalizeProductionDeploy: %v", err)
	}

	want := []string{"reconcile", "stage", "promote"}
	if len(fake.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	for i, c := range want {
		if fake.calls[i] != c {
			t.Errorf("calls[%d] = %q, want %q (full sequence: %v)", i, fake.calls[i], c, fake.calls)
		}
	}
}

func TestFinalizeProductionDeploy_AppFailureAbortsPromote(t *testing.T) {
	fake := &recordingRootTier{}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", Err: errors.New("app-deploy stack failed")},
	}

	_, err := finalizeProductionDeploy(ctx, fake, []edge.RootTierSpec{{Version: "v1"}}, nil, "promo1", 100, results)
	if err == nil {
		t.Fatal("expected an error when one app's deploy failed")
	}

	if len(fake.staged) != 1 {
		t.Errorf("staged = %d, want 1 (only the successful app)", len(fake.staged))
	}
	if len(fake.promotions) != 0 {
		t.Errorf("promotions = %d, want 0: a failed app must abort the promote", len(fake.promotions))
	}
}

func TestFinalizeProductionDeploy_SecondDeployProducesNewPromotionRetainingPrior(t *testing.T) {
	fake := &recordingRootTier{}
	ctx := context.Background()
	specs := []edge.RootTierSpec{{Version: "v1"}}
	results := []appDeployResult{{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}}}

	state, err := finalizeProductionDeploy(ctx, fake, specs, nil, "promo1", 100, results)
	if err != nil {
		t.Fatalf("first finalizeProductionDeploy: %v", err)
	}

	results2 := []appDeployResult{{App: "web", BuildID: "b2", Record: edge.DeploymentRecord{App: "web", BuildID: "b2"}}}
	if _, err := finalizeProductionDeploy(ctx, fake, specs, state, "promo2", 200, results2); err != nil {
		t.Fatalf("second finalizeProductionDeploy: %v", err)
	}

	if len(fake.promotions) != 2 {
		t.Fatalf("promotions = %d, want 2 (both retained)", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" || fake.promotions[1].PromotionID != "promo2" {
		t.Errorf("promotions = %+v, want promo1 then promo2", fake.promotions)
	}
}

// orderTrackingRootTier wraps recordingRootTier to additionally record the
// relative order of reconcile/stage/promote calls, which recordingRootTier's
// own per-kind slices cannot express on their own.
type orderTrackingRootTier struct {
	*recordingRootTier
	calls []string
}

func (f *orderTrackingRootTier) ReconcileRootTier(ctx context.Context, spec edge.RootTierSpec, prior edge.RootTierState) (edge.RootTierState, error) {
	f.calls = append(f.calls, "reconcile")
	return f.recordingRootTier.ReconcileRootTier(ctx, spec, prior)
}

func (f *orderTrackingRootTier) PutStaged(ctx context.Context, state edge.RootTierState, record edge.DeploymentRecord) error {
	f.calls = append(f.calls, "stage")
	return f.recordingRootTier.PutStaged(ctx, state, record)
}

func (f *orderTrackingRootTier) Promote(ctx context.Context, state edge.RootTierState, promotion edge.Promotion) error {
	f.calls = append(f.calls, "promote")
	return f.recordingRootTier.Promote(ctx, state, promotion)
}

func TestReconcileRootTier_ThreadsStateAcrossMultipleSpecs(t *testing.T) {
	fake := &recordingRootTier{}
	ctx := context.Background()
	specs := []edge.RootTierSpec{
		{Version: "v1", GenericName: "web-generic"},
		{Version: "v1", GenericName: "admin-generic"},
	}

	state, err := reconcileRootTier(ctx, fake, specs, nil)
	if err != nil {
		t.Fatalf("reconcileRootTier: %v", err)
	}
	if len(fake.reconciles) != 2 {
		t.Fatalf("reconciles = %d, want 2 (one per spec)", len(fake.reconciles))
	}
	if state[edge.RootTierKeyEndpoint] == "" {
		t.Error("expected a non-empty reconciled state")
	}
}

func TestReconcileRootTier_NoSpecsReturnsPriorUnchanged(t *testing.T) {
	fake := &recordingRootTier{}
	ctx := context.Background()
	prior := edge.RootTierState{edge.RootTierKeyEndpoint: "https://prior"}

	state, err := reconcileRootTier(ctx, fake, nil, prior)
	if err != nil {
		t.Fatalf("reconcileRootTier: %v", err)
	}
	if len(fake.reconciles) != 0 {
		t.Errorf("reconciles = %d, want 0", len(fake.reconciles))
	}
	if state[edge.RootTierKeyEndpoint] != "https://prior" {
		t.Errorf("state = %v, want prior unchanged", state)
	}
}
