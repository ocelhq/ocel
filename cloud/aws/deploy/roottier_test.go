package deploy

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// recordingRootTier is recordingEdge's edge.RootTier counterpart (ADR
// 0001/0002): it records every reconcile and store call so host
// orchestration tests (ticket ocelhq-u8h.5) can assert the reconcile-then-
// stage-then-promote sequence without any Cloudflare API. Reconcile
// simulates the real provider's version-stamp gate purely from its own
// in-memory state — a no-op unless spec.Version differs from what the last
// reconcile "deployed" — and every store call rejects a write-secret that
// doesn't match what reconcile last minted, catching a host that calls a
// store operation before reconciling the root tier.
type recordingRootTier struct {
	recordingEdge

	reconciles []edge.RootTierSpec
	redeploys  int
	secret     string
	version    string

	staged     []edge.DeploymentRecord
	promotions []edge.Promotion
	pruned     []int

	history     []edge.HistoryEntry
	pruneResult edge.PruneResult
}

var _ edge.RootTier = (*recordingRootTier)(nil)

const fakeStoreEndpoint = "https://store.fake"

func (f *recordingRootTier) ReconcileRootTier(_ context.Context, spec edge.RootTierSpec, prior edge.RootTierState) (edge.RootTierState, error) {
	f.reconciles = append(f.reconciles, spec)
	if prior != nil && f.version == spec.Version {
		return prior, nil
	}
	f.redeploys++
	f.version = spec.Version
	if f.secret == "" {
		f.secret = "fake-secret"
	}
	return edge.RootTierState{
		edge.RootTierKeyEndpoint:    fakeStoreEndpoint,
		edge.RootTierKeyWriteSecret: f.secret,
	}, nil
}

func (f *recordingRootTier) checkAuth(state edge.RootTierState) error {
	if f.secret == "" || state[edge.RootTierKeyWriteSecret] != f.secret {
		return fmt.Errorf("recordingRootTier: unauthenticated store call; reconcile the root tier first")
	}
	return nil
}

func (f *recordingRootTier) PutStaged(_ context.Context, state edge.RootTierState, record edge.DeploymentRecord) error {
	if err := f.checkAuth(state); err != nil {
		return err
	}
	f.staged = append(f.staged, record)
	return nil
}

func (f *recordingRootTier) Promote(_ context.Context, state edge.RootTierState, promotion edge.Promotion) error {
	if err := f.checkAuth(state); err != nil {
		return err
	}
	f.promotions = append(f.promotions, promotion)
	return nil
}

func (f *recordingRootTier) History(_ context.Context, state edge.RootTierState) ([]edge.HistoryEntry, error) {
	if err := f.checkAuth(state); err != nil {
		return nil, err
	}
	return f.history, nil
}

func (f *recordingRootTier) DeletePromotionArtifacts(_ context.Context, state edge.RootTierState, keepN int) (edge.PruneResult, error) {
	if err := f.checkAuth(state); err != nil {
		return edge.PruneResult{}, err
	}
	f.pruned = append(f.pruned, keepN)
	return f.pruneResult, nil
}

func TestRecordingRootTier_ReconcileIsANoOpWhenVersionUnchanged(t *testing.T) {
	f := &recordingRootTier{}
	ctx := context.Background()
	spec := edge.RootTierSpec{Version: "v1"}

	state, err := f.ReconcileRootTier(ctx, spec, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}
	if f.redeploys != 1 {
		t.Fatalf("redeploys = %d, want 1 after the first reconcile", f.redeploys)
	}

	again, err := f.ReconcileRootTier(ctx, spec, state)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}
	if f.redeploys != 1 {
		t.Errorf("redeploys = %d, want 1: an unchanged version must be a no-op", f.redeploys)
	}
	if again[edge.RootTierKeyWriteSecret] != state[edge.RootTierKeyWriteSecret] {
		t.Errorf("a no-op reconcile must hand back the same state unchanged")
	}
	if len(f.reconciles) != 2 {
		t.Errorf("expected both reconcile attempts recorded, got %d", len(f.reconciles))
	}
}

func TestRecordingRootTier_ReconcileRedeploysOnVersionBump(t *testing.T) {
	f := &recordingRootTier{}
	ctx := context.Background()

	state, err := f.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}
	if _, err := f.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v2"}, state); err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}
	if f.redeploys != 2 {
		t.Errorf("redeploys = %d, want 2: a version bump must not be a no-op", f.redeploys)
	}
}

func TestRecordingRootTier_StoreOpsRejectAnUnreconciledState(t *testing.T) {
	f := &recordingRootTier{}
	ctx := context.Background()
	record := edge.DeploymentRecord{App: "web", BuildID: "b1"}

	if err := f.PutStaged(ctx, edge.RootTierState{}, record); err == nil {
		t.Error("expected PutStaged to reject a state no reconcile ever produced")
	}
	if len(f.staged) != 0 {
		t.Errorf("expected no record staged, got %v", f.staged)
	}
}

func TestRecordingRootTier_StoreOpsRecordCallsAfterReconcile(t *testing.T) {
	f := &recordingRootTier{
		history:     []edge.HistoryEntry{{Promotion: edge.Promotion{PromotionID: "p1"}, Active: true}},
		pruneResult: edge.PruneResult{RemovedPromotionIDs: []string{"p0"}},
	}
	ctx := context.Background()

	state, err := f.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}

	record := edge.DeploymentRecord{App: "web", BuildID: "b1"}
	if err := f.PutStaged(ctx, state, record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1, Builds: map[string]string{"web": "b1"}}
	if err := f.Promote(ctx, state, promotion); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	history, err := f.History(ctx, state)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	result, err := f.DeletePromotionArtifacts(ctx, state, 3)
	if err != nil {
		t.Fatalf("DeletePromotionArtifacts: %v", err)
	}

	if len(f.staged) != 1 || !reflect.DeepEqual(f.staged[0], record) {
		t.Errorf("staged = %v, want [%v]", f.staged, record)
	}
	if len(f.promotions) != 1 || !reflect.DeepEqual(f.promotions[0], promotion) {
		t.Errorf("promotions = %v, want [%v]", f.promotions, promotion)
	}
	if len(history) != 1 || history[0].PromotionID != "p1" {
		t.Errorf("History = %v", history)
	}
	if len(f.pruned) != 1 || f.pruned[0] != 3 {
		t.Errorf("pruned = %v, want [3]", f.pruned)
	}
	if len(result.RemovedPromotionIDs) != 1 || result.RemovedPromotionIDs[0] != "p0" {
		t.Errorf("DeletePromotionArtifacts result = %+v", result)
	}
}
