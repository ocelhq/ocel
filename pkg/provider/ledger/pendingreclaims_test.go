package ledger

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/router"
)

func promotionNamed(id string) router.Promotion {
	return router.Promotion{PromotionID: id, Releases: map[string]string{"web": "web-" + id}}
}

func promotionIDs(promotions []RecordedPromotion) string {
	return strings.Join(collectPromotionIDs(promotions), ",")
}

func TestAReclaimLeftPendingIsNamedByTheNextPruneAndThenForgotten(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1", "p2", "p3", "p4")
	_, dropped, err := l.change(ctx, router.ResolvePointer(""), func(current Pointer) (Pointer, []RecordedPromotion, error) {
		kept, dropped := current.Retain(2)
		return kept, dropped, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.RecordPendingReclaims(ctx, "", dropped); err != nil {
		t.Fatal(err)
	}

	result, _, err := l.Prune(ctx, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.RemovedPromotionIDs, ","); got != "p2,p1" {
		t.Errorf("removed = %s, want the promotions whose reclaim was left pending", got)
	}
	if got := strings.Join(result.UnnamedRecordKeys, ","); got != "record:web/web-p1,record:web/web-p2" {
		t.Errorf("unnamed record keys = %s, want the records of the pending reclaims", got)
	}
	after, err := l.Read(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.PendingReclaims) != 0 {
		t.Errorf("pending reclaims after the prune named them = %v, want none: the prune reclaims them now", promotionIDs(after.PendingReclaims))
	}
}

func TestAPendingReclaimIsRecordedOnceAndNeverForAPromotionThePointerStillKeeps(t *testing.T) {
	pointer := Pointer{Name: "@", Promotions: []RecordedPromotion{{Promotion: promotionNamed("p3")}}}
	p1 := RecordedPromotion{Promotion: promotionNamed("p1")}
	p3 := RecordedPromotion{Promotion: promotionNamed("p3")}

	next := pointer.RecordPendingReclaims([]RecordedPromotion{p1, p3}).RecordPendingReclaims([]RecordedPromotion{p1})
	if got := promotionIDs(next.PendingReclaims); got != "p1" {
		t.Errorf("pending reclaims = %s, want p1 once and not p3, which the pointer still keeps", got)
	}
}

func TestAPointerHoldingOnlyPendingReclaimsIsKept(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	if err := l.RecordPendingReclaims(ctx, "pr-1", []RecordedPromotion{{Promotion: promotionNamed("p1")}}); err != nil {
		t.Fatal(err)
	}
	read, err := l.Read(ctx, "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := promotionIDs(read.PendingReclaims); got != "p1" {
		t.Errorf("pending reclaims = %s, want p1 kept on a pointer naming nothing else", got)
	}
}
