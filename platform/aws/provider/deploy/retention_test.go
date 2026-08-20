package deploy

import (
	"reflect"
	"testing"
)

func TestRetention(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		history     []HistoryEntry
		keepN       int
		wantKeep    []string
		wantCollect []string
	}{
		{
			name: "keeps the window and pins active",
			history: []HistoryEntry{
				{Promotion: Promotion{PromotionID: "p5"}},
				{Promotion: Promotion{PromotionID: "p4"}},
				{Promotion: Promotion{PromotionID: "p3"}},
				{Promotion: Promotion{PromotionID: "p2"}},
				{Promotion: Promotion{PromotionID: "p1"}, Active: true},
			},
			keepN:       2,
			wantKeep:    []string{"p5", "p4", "p1"},
			wantCollect: []string{"p3", "p2"},
		},
		{
			name: "active inside the window is not duplicated",
			history: []HistoryEntry{
				{Promotion: Promotion{PromotionID: "p3"}},
				{Promotion: Promotion{PromotionID: "p2"}, Active: true},
				{Promotion: Promotion{PromotionID: "p1"}},
			},
			keepN:       2,
			wantKeep:    []string{"p3", "p2"},
			wantCollect: []string{"p1"},
		},
		{
			name: "a non-positive keepN still pins active",
			history: []HistoryEntry{
				{Promotion: Promotion{PromotionID: "p2"}},
				{Promotion: Promotion{PromotionID: "p1"}, Active: true},
			},
			keepN:       0,
			wantKeep:    []string{"p1"},
			wantCollect: []string{"p2"},
		},
		{
			name:        "empty history",
			history:     nil,
			keepN:       5,
			wantKeep:    nil,
			wantCollect: nil,
		},
		{
			name: "a window larger than history keeps everything",
			history: []HistoryEntry{
				{Promotion: Promotion{PromotionID: "p2"}, Active: true},
				{Promotion: Promotion{PromotionID: "p1"}},
			},
			keepN:       10,
			wantKeep:    []string{"p2", "p1"},
			wantCollect: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keep, collect := Retention(tc.history, tc.keepN)
			if !reflect.DeepEqual(keep, tc.wantKeep) {
				t.Errorf("keep = %v, want %v", keep, tc.wantKeep)
			}
			if !reflect.DeepEqual(collect, tc.wantCollect) {
				t.Errorf("collect = %v, want %v", collect, tc.wantCollect)
			}
		})
	}
}
