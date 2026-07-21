package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestRollbackTarget_NoArgSelectsImmediatelyPreviousPromotion(t *testing.T) {
	history := []edge.HistoryEntry{
		{Promotion: edge.Promotion{PromotionID: "promo-2"}, Active: true},
		{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
	}

	target, err := RollbackTarget(history, "")
	if err != nil {
		t.Fatalf("RollbackTarget: %v", err)
	}
	if target.PromotionID != "promo-1" {
		t.Errorf("target = %q, want %q", target.PromotionID, "promo-1")
	}
}

func TestRollbackTarget_ToSelectsNamedPromotion(t *testing.T) {
	history := []edge.HistoryEntry{
		{Promotion: edge.Promotion{PromotionID: "promo-3"}, Active: true},
		{Promotion: edge.Promotion{PromotionID: "promo-2"}, Active: false},
		{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
	}

	target, err := RollbackTarget(history, "promo-1")
	if err != nil {
		t.Fatalf("RollbackTarget: %v", err)
	}
	if target.PromotionID != "promo-1" {
		t.Errorf("target = %q, want %q", target.PromotionID, "promo-1")
	}
}

func TestRollbackTarget_UnknownToErrorsClearly(t *testing.T) {
	history := []edge.HistoryEntry{
		{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
	}

	_, err := RollbackTarget(history, "no-such-promotion")
	if err == nil {
		t.Fatal("expected an error for an unknown promotion id")
	}
}

func TestRollbackTarget_NoArgErrorsWhenActiveIsOldestPromotion(t *testing.T) {
	history := []edge.HistoryEntry{
		{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
	}

	_, err := RollbackTarget(history, "")
	if err == nil {
		t.Fatal("expected an error when there is no earlier promotion to roll back to")
	}
}

func TestRollbackTarget_NoArgErrorsWhenNoActivePromotion(t *testing.T) {
	history := []edge.HistoryEntry{
		{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
	}

	_, err := RollbackTarget(history, "")
	if err == nil {
		t.Fatal("expected an error when the project has no active promotion")
	}
}

func TestRollback_PromotesTheTargetUnderAFreshTimestamp(t *testing.T) {
	fake := &recordingRootTier{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-2", Ts: 200, Builds: map[string]string{"web": "b2"}}, Active: true},
			{Promotion: edge.Promotion{PromotionID: "promo-1", Ts: 100, Builds: map[string]string{"web": "b1"}}, Active: false},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}

	promoted, err := Rollback(ctx, fake, state, "", 999)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if promoted.PromotionID != "promo-1" {
		t.Errorf("promoted = %q, want %q", promoted.PromotionID, "promo-1")
	}
	if promoted.Ts != 999 {
		t.Errorf("promoted.Ts = %d, want the fresh timestamp 999", promoted.Ts)
	}
	if promoted.Builds["web"] != "b1" {
		t.Errorf("promoted.Builds = %v, want promo-1's builds", promoted.Builds)
	}

	if len(fake.promotions) != 1 || fake.promotions[0].PromotionID != "promo-1" {
		t.Errorf("promotions = %v, want a single re-promotion of promo-1", fake.promotions)
	}
}

func TestRollback_ToASpecificPromotion(t *testing.T) {
	fake := &recordingRootTier{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-3", Ts: 300}, Active: true},
			{Promotion: edge.Promotion{PromotionID: "promo-2", Ts: 200}, Active: false},
			{Promotion: edge.Promotion{PromotionID: "promo-1", Ts: 100}, Active: false},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}

	promoted, err := Rollback(ctx, fake, state, "promo-1", 999)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if promoted.PromotionID != "promo-1" {
		t.Errorf("promoted = %q, want %q", promoted.PromotionID, "promo-1")
	}
}

func TestRollback_UnknownToErrorsAndNeverPromotes(t *testing.T) {
	fake := &recordingRootTier{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootTier(ctx, edge.RootTierSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootTier: %v", err)
	}

	if _, err := Rollback(ctx, fake, state, "no-such-promotion", 999); err == nil {
		t.Fatal("expected an error for an unknown promotion id")
	}
	if len(fake.promotions) != 0 {
		t.Errorf("promotions = %v, want none: an unknown target must never promote", fake.promotions)
	}
}
