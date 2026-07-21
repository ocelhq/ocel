package deploy

import (
	"reflect"
	"testing"
)

func TestRetention_KeepsWindowAndPinsActive(t *testing.T) {
	// Newest-first, mirroring the store's history() order. p1 is active but
	// old enough to fall outside a keepN=2 window.
	history := []HistoryEntry{
		{Promotion: Promotion{PromotionID: "p5"}},
		{Promotion: Promotion{PromotionID: "p4"}},
		{Promotion: Promotion{PromotionID: "p3"}},
		{Promotion: Promotion{PromotionID: "p2"}},
		{Promotion: Promotion{PromotionID: "p1"}, Active: true},
	}

	keep, collect := Retention(history, 2)

	wantKeep := []string{"p5", "p4", "p1"}
	if !reflect.DeepEqual(keep, wantKeep) {
		t.Errorf("keep = %v, want %v", keep, wantKeep)
	}
	wantCollect := []string{"p3", "p2"}
	if !reflect.DeepEqual(collect, wantCollect) {
		t.Errorf("collect = %v, want %v", collect, wantCollect)
	}
}

func TestRetention_ActiveInsideWindowNotDuplicated(t *testing.T) {
	history := []HistoryEntry{
		{Promotion: Promotion{PromotionID: "p3"}},
		{Promotion: Promotion{PromotionID: "p2"}, Active: true},
		{Promotion: Promotion{PromotionID: "p1"}},
	}

	keep, collect := Retention(history, 2)

	wantKeep := []string{"p3", "p2"}
	if !reflect.DeepEqual(keep, wantKeep) {
		t.Errorf("keep = %v, want %v", keep, wantKeep)
	}
	wantCollect := []string{"p1"}
	if !reflect.DeepEqual(collect, wantCollect) {
		t.Errorf("collect = %v, want %v", collect, wantCollect)
	}
}

func TestRetention_NonPositiveKeepNStillPinsActive(t *testing.T) {
	history := []HistoryEntry{
		{Promotion: Promotion{PromotionID: "p2"}},
		{Promotion: Promotion{PromotionID: "p1"}, Active: true},
	}

	keep, collect := Retention(history, 0)
	if !reflect.DeepEqual(keep, []string{"p1"}) {
		t.Errorf("keep = %v, want [p1]", keep)
	}
	if !reflect.DeepEqual(collect, []string{"p2"}) {
		t.Errorf("collect = %v, want [p2]", collect)
	}

	keepNeg, collectNeg := Retention(history, -5)
	if !reflect.DeepEqual(keepNeg, keep) || !reflect.DeepEqual(collectNeg, collect) {
		t.Errorf("negative keepN should behave like 0: keep=%v collect=%v", keepNeg, collectNeg)
	}
}

func TestRetention_EmptyHistory(t *testing.T) {
	keep, collect := Retention(nil, 5)
	if keep != nil || collect != nil {
		t.Errorf("expected nil/empty results for empty history, got keep=%v collect=%v", keep, collect)
	}
}

func TestRetention_WindowLargerThanHistoryKeepsEverything(t *testing.T) {
	history := []HistoryEntry{
		{Promotion: Promotion{PromotionID: "p2"}, Active: true},
		{Promotion: Promotion{PromotionID: "p1"}},
	}

	keep, collect := Retention(history, 10)
	if !reflect.DeepEqual(keep, []string{"p2", "p1"}) {
		t.Errorf("keep = %v, want [p2 p1]", keep)
	}
	if collect != nil {
		t.Errorf("collect = %v, want nil", collect)
	}
}
