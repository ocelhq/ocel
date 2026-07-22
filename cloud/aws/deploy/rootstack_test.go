package deploy

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// recordingRootStack is recordingEdge's edge.RootStack counterpart (ADR
// 0001/0002): it records every reconcile and store call so host
// orchestration tests (ticket ocelhq-u8h.5) can assert the reconcile-then-
// stage-then-promote sequence without any Cloudflare API. Reconcile
// simulates the real provider's version-stamp gate purely from its own
// in-memory state — a no-op unless spec.Version differs from what the last
// reconcile "deployed" — and every store call rejects a write-secret that
// doesn't match what reconcile last minted, catching a host that calls a
// store operation before reconciling the root stack.
type recordingRootStack struct {
	recordingEdge

	reconciles []edge.RootStackSpec
	redeploys  int
	secret     string
	version    string

	staged            []edge.DeploymentRecord
	promotions        []edge.Promotion
	promotePointers   []string
	pruned            []int
	prunePointers     []string
	removedPointers   []string
	historyPointers   []string
	destroyed         int
	destroyedWorkers  []string
	destroyedInstance int
	listedPrefixes    []string

	history         []edge.HistoryEntry
	deployedWorkers []string
	pruneResult     edge.PruneResult
}

var _ edge.RootStack = (*recordingRootStack)(nil)

const fakeStoreEndpoint = "https://store.fake"

func (f *recordingRootStack) ReconcileRootStack(_ context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	f.reconciles = append(f.reconciles, spec)
	if prior != nil && f.version == spec.Version {
		return prior, nil
	}
	f.redeploys++
	f.version = spec.Version
	if f.secret == "" {
		f.secret = "fake-secret"
	}
	return edge.RootStackState{
		edge.RootStackKeySlug:     spec.Slug,
		edge.RootStackKeyEndpoint: fakeStoreEndpoint,
		edge.RootStackKeySecret:   f.secret,
	}, nil
}

func (f *recordingRootStack) checkAuth(state edge.RootStackState) error {
	if f.secret == "" || state[edge.RootStackKeySecret] != f.secret {
		return fmt.Errorf("recordingRootStack: unauthenticated store call; reconcile the root stack first")
	}
	return nil
}

func (f *recordingRootStack) PutStaged(_ context.Context, state edge.RootStackState, record edge.DeploymentRecord) error {
	if err := f.checkAuth(state); err != nil {
		return err
	}
	f.staged = append(f.staged, record)
	return nil
}

func (f *recordingRootStack) Promote(_ context.Context, state edge.RootStackState, promotion edge.Promotion, pointer string) error {
	if err := f.checkAuth(state); err != nil {
		return err
	}
	f.promotions = append(f.promotions, promotion)
	f.promotePointers = append(f.promotePointers, pointer)
	return nil
}

func (f *recordingRootStack) History(_ context.Context, state edge.RootStackState, pointer string) ([]edge.HistoryEntry, error) {
	if err := f.checkAuth(state); err != nil {
		return nil, err
	}
	f.historyPointers = append(f.historyPointers, pointer)
	return f.history, nil
}

func (f *recordingRootStack) DeletePromotionArtifacts(_ context.Context, state edge.RootStackState, keepN int, pointer string) (edge.PruneResult, error) {
	if err := f.checkAuth(state); err != nil {
		return edge.PruneResult{}, err
	}
	f.pruned = append(f.pruned, keepN)
	f.prunePointers = append(f.prunePointers, pointer)
	return f.pruneResult, nil
}

func (f *recordingRootStack) RemovePointer(_ context.Context, state edge.RootStackState, pointer string) (edge.PruneResult, error) {
	if err := f.checkAuth(state); err != nil {
		return edge.PruneResult{}, err
	}
	f.removedPointers = append(f.removedPointers, pointer)
	return f.pruneResult, nil
}

func (f *recordingRootStack) DestroyRootStack(_ context.Context, workers []string) error {
	f.destroyedWorkers = append(f.destroyedWorkers, workers...)
	f.destroyed++
	return nil
}

func (f *recordingRootStack) ListDeployedWorkers(_ context.Context, prefix string) ([]string, error) {
	f.listedPrefixes = append(f.listedPrefixes, prefix)
	var names []string
	for _, name := range f.deployedWorkers {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (f *recordingRootStack) DestroyInstance(_ context.Context, state edge.RootStackState) error {
	if err := f.checkAuth(state); err != nil {
		return err
	}
	f.destroyedInstance++
	return nil
}

func TestRecordingRootStack_ReconcileIsANoOpWhenVersionUnchanged(t *testing.T) {
	f := &recordingRootStack{}
	ctx := context.Background()
	spec := edge.RootStackSpec{Version: "v1"}

	state, err := f.ReconcileRootStack(ctx, spec, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	if f.redeploys != 1 {
		t.Fatalf("redeploys = %d, want 1 after the first reconcile", f.redeploys)
	}

	again, err := f.ReconcileRootStack(ctx, spec, state)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	if f.redeploys != 1 {
		t.Errorf("redeploys = %d, want 1: an unchanged version must be a no-op", f.redeploys)
	}
	if again[edge.RootStackKeySecret] != state[edge.RootStackKeySecret] {
		t.Errorf("a no-op reconcile must hand back the same state unchanged")
	}
	if len(f.reconciles) != 2 {
		t.Errorf("expected both reconcile attempts recorded, got %d", len(f.reconciles))
	}
}

func TestRecordingRootStack_ReconcileRedeploysOnVersionBump(t *testing.T) {
	f := &recordingRootStack{}
	ctx := context.Background()

	state, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	if _, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v2"}, state); err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	if f.redeploys != 2 {
		t.Errorf("redeploys = %d, want 2: a version bump must not be a no-op", f.redeploys)
	}
}

func TestRecordingRootStack_StoreOpsRejectAnUnreconciledState(t *testing.T) {
	f := &recordingRootStack{}
	ctx := context.Background()
	record := edge.DeploymentRecord{App: "web", BuildID: "b1"}

	if err := f.PutStaged(ctx, edge.RootStackState{}, record); err == nil {
		t.Error("expected PutStaged to reject a state no reconcile ever produced")
	}
	if len(f.staged) != 0 {
		t.Errorf("expected no record staged, got %v", f.staged)
	}
}

func TestRecordingRootStack_StoreOpsRecordCallsAfterReconcile(t *testing.T) {
	f := &recordingRootStack{
		history:     []edge.HistoryEntry{{Promotion: edge.Promotion{PromotionID: "p1"}, Active: true}},
		pruneResult: edge.PruneResult{RemovedPromotionIDs: []string{"p0"}},
	}
	ctx := context.Background()

	state, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	record := edge.DeploymentRecord{App: "web", BuildID: "b1"}
	if err := f.PutStaged(ctx, state, record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1, Builds: map[string]string{"web": "b1"}}
	if err := f.Promote(ctx, state, promotion, ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	history, err := f.History(ctx, state, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	result, err := f.DeletePromotionArtifacts(ctx, state, 3, "")
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
