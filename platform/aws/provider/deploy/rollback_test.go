package deploy

import (
	"context"
	"reflect"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRollbackTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history []edge.HistoryEntry
		to      string
		tag     string
		want    string
		wantErr bool
	}{
		{
			name: "no arg selects immediately previous promotion",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-2"}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
			},
			want: "promo-1",
		},
		{
			name: "to selects named promotion",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-3"}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-2"}, Active: false},
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
			},
			to:   "promo-1",
			want: "promo-1",
		},
		{
			name: "tag selects tagged promotion",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-3"}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-2", Tag: "v1.2.3"}, Active: false},
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
			},
			tag:  "v1.2.3",
			want: "promo-2",
		},
		{
			name: "unknown tag errors clearly",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1"}, Active: true},
			},
			tag:     "no-such-tag",
			wantErr: true,
		},
		{
			name: "unknown to errors clearly",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
			},
			to:      "no-such-promotion",
			wantErr: true,
		},
		{
			name: "no arg errors when active is oldest promotion",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
			},
			wantErr: true,
		},
		{
			name: "no arg errors when no active promotion",
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: false},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, err := RollbackTarget(tc.history, tc.to, tc.tag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RollbackTarget = %q, want an error", target.PromotionID)
				}
				return
			}
			if err != nil {
				t.Fatalf("RollbackTarget: %v", err)
			}
			if target.PromotionID != tc.want {
				t.Errorf("target = %q, want %q", target.PromotionID, tc.want)
			}
		})
	}
}

func TestRollback(t *testing.T) {
	t.Run("promotes the target under a fresh timestamp", func(t *testing.T) {
		t.Parallel()
		fake := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-2", Ts: 200, Builds: map[string]string{"web": "b2"}}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-1", Ts: 100, Builds: map[string]string{"web": "b1"}}, Active: false},
			},
		}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		promoted, err := Rollback(ctx, fake, state, "", "", 999)
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
	})

	t.Run("across a rotation re-points at the rotated identity", func(t *testing.T) {
		t.Parallel()
		fake := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "p3", Ts: 300, Builds: map[string]string{"web": "B2"}}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "p2", Ts: 200, Builds: map[string]string{"web": "B1~fp2"}}, Active: false},
				{Promotion: edge.Promotion{PromotionID: "p1", Ts: 100, Builds: map[string]string{"web": "B1"}}, Active: false},
			},
		}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		promoted, err := Rollback(ctx, fake, state, "", "", 999)
		if err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if got := promoted.Builds["web"]; got != "B1~fp2" {
			t.Errorf("promoted.Builds[web] = %q, want %q — the rotated Deployment, values and all", got, "B1~fp2")
		}
		if len(fake.promotions) != 1 {
			t.Fatalf("promotions = %v, want a single re-promotion", fake.promotions)
		}
		if got := fake.promotions[0].Builds["web"]; got != "B1~fp2" {
			t.Errorf("re-promotion Builds[web] = %q, want %q", got, "B1~fp2")
		}
		if got, want := fake.promotions[0].Builds, fake.history[1].Promotion.Builds; !reflect.DeepEqual(got, want) {
			t.Errorf("re-promotion Builds = %v, want p2's own %v — the artifact that identity names is what carries the values back", got, want)
		}
	})

	t.Run("to a specific promotion", func(t *testing.T) {
		t.Parallel()
		fake := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-3", Ts: 300}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-2", Ts: 200}, Active: false},
				{Promotion: edge.Promotion{PromotionID: "promo-1", Ts: 100}, Active: false},
			},
		}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		promoted, err := Rollback(ctx, fake, state, "promo-1", "", 999)
		if err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if promoted.PromotionID != "promo-1" {
			t.Errorf("promoted = %q, want %q", promoted.PromotionID, "promo-1")
		}
	})

	t.Run("by tag carries the tag onto the re-promotion", func(t *testing.T) {
		t.Parallel()
		fake := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-2", Ts: 200, Builds: map[string]string{"web": "b2"}}, Active: true},
				{Promotion: edge.Promotion{PromotionID: "promo-1", Ts: 100, Tag: "v1.2.3", Builds: map[string]string{"web": "b1"}}, Active: false},
			},
		}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		promoted, err := Rollback(ctx, fake, state, "", "v1.2.3", 999)
		if err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if promoted.PromotionID != "promo-1" {
			t.Errorf("promoted = %q, want %q", promoted.PromotionID, "promo-1")
		}
		if promoted.Tag != "v1.2.3" {
			t.Errorf("promoted.Tag = %q, want the target's tag preserved through rollback", promoted.Tag)
		}
		if len(fake.promotions) != 1 || fake.promotions[0].Tag != "v1.2.3" {
			t.Errorf("promotions = %v, want the re-promote to carry the tag", fake.promotions)
		}
	})

	t.Run("unknown to errors and never promotes", func(t *testing.T) {
		t.Parallel()
		fake := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1"}, Active: true},
			},
		}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if _, err := Rollback(ctx, fake, state, "no-such-promotion", "", 999); err == nil {
			t.Fatal("expected an error for an unknown promotion id")
		}
		if len(fake.promotions) != 0 {
			t.Errorf("promotions = %v, want none: an unknown target must never promote", fake.promotions)
		}
	})
}
