package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func promotion(id string) router.Promotion {
	return router.Promotion{PromotionID: id, Builds: map[string]string{"web": "web-" + id}}
}

func promoted(t *testing.T, promotions ...router.Promotion) ledger.Pointer {
	t.Helper()
	pointer := ledger.Pointer{Name: router.DefaultPointer}
	for _, next := range promotions {
		var err error
		pointer, _, err = pointer.Promote(next, pointer.Active, ledger.KeptPromotions)
		if err != nil {
			t.Fatalf("Promote(%s) = %v", next.PromotionID, err)
		}
	}
	return pointer
}

func refusedWith(t *testing.T, err error, code refusal.Code) refusal.Refusal {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != code {
		t.Fatalf("err = %v, want a %s refusal", err, code)
	}
	return refused
}

func TestAPromoteOverWhatThePointerNamesMakesItActiveAndRecordsWhatItDisplaced(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"))

	if pointer.Active != "p2" {
		t.Errorf("Active = %q, want p2", pointer.Active)
	}
	if len(pointer.Entries) != 2 || pointer.Entries[0].PromotionID != "p2" || pointer.Entries[1].PromotionID != "p1" {
		t.Fatalf("Entries = %+v, want p2 then p1", pointer.Entries)
	}
	if pointer.Entries[0].Displaced != "p1" || pointer.Entries[1].Displaced != "" {
		t.Errorf("Displaced = %q, %q, want p1 and nothing", pointer.Entries[0].Displaced, pointer.Entries[1].Displaced)
	}
	if pointer.Entries[0].Seq != 2 || pointer.Entries[1].Seq != 1 || pointer.Seq != 2 {
		t.Errorf("Seq = %d, %d on a pointer at %d, want 2, 1 on a pointer at 2", pointer.Entries[0].Seq, pointer.Entries[1].Seq, pointer.Seq)
	}
}

func TestAPromoteOverAPromotionThePointerNoLongerNamesIsRefusedBusy(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"))

	after, _, err := pointer.Promote(promotion("p3"), "p1", ledger.KeptPromotions)
	refusedWith(t, err, refusal.CodeBusy)
	if after.Active != "" || len(after.Entries) != 0 {
		t.Errorf("a refused promote returned %+v, want nothing", after)
	}
	if pointer.Active != "p2" || len(pointer.Entries) != 2 {
		t.Errorf("a refused promote changed the pointer it was asked of: %+v", pointer)
	}
}

func ids(entries []ledger.Entry) string {
	named := make([]string, 0, len(entries))
	for _, entry := range entries {
		named = append(named, entry.PromotionID)
	}
	return strings.Join(named, ",")
}

func TestAPromoteKeepsTheNewestPromotionsAndDropsTheRest(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"), promotion("p3"))

	after, dropped, err := pointer.Promote(promotion("p4"), "p3", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(after.Entries); got != "p4,p3" {
		t.Errorf("kept %s, want p4,p3", got)
	}
	if got := ids(dropped); got != "p2,p1" {
		t.Errorf("dropped %s, want p2,p1", got)
	}
}

func recorded(active string, promotionIDs ...string) ledger.Pointer {
	pointer := ledger.Pointer{Name: router.DefaultPointer, Active: active}
	for _, id := range promotionIDs {
		pointer.Entries = append(pointer.Entries, ledger.Entry{Promotion: promotion(id)})
	}
	return pointer
}

func TestRetainNeverDropsWhatIsServingNow(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("p2", "p4", "p3", "p2", "p1").Retain(1)
	if got := ids(kept.Entries); got != "p4,p2" {
		t.Errorf("kept %s, want p4 and the promotion the pointer serves however old it is", got)
	}
	if got := ids(dropped); got != "p3,p1" {
		t.Errorf("dropped %s, want p3,p1", got)
	}
}

func TestRetainKeepingNoneStillKeepsWhatIsServing(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("p1", "p2", "p1").Retain(0)
	if got := ids(kept.Entries); got != "p1" {
		t.Errorf("kept %s, want only the active promotion", got)
	}
	if got := ids(dropped); got != "p2" {
		t.Errorf("dropped %s, want p2", got)
	}
}

func TestRetainOverNothingDropsNothing(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("").Retain(3)
	if len(kept.Entries) != 0 || len(dropped) != 0 {
		t.Errorf("Retain() over an empty pointer = %+v / %+v, want nothing either way", kept.Entries, dropped)
	}
}

func TestAPromoteRefusesATagAnotherPromotionOnThePointerHolds(t *testing.T) {
	t.Parallel()

	tagged := promotion("p1")
	tagged.Tag = "v1"
	pointer := promoted(t, tagged, promotion("p2"))

	again := promotion("p3")
	again.Tag = "v1"
	_, _, err := pointer.Promote(again, "p2", ledger.KeptPromotions)
	refused := refusedWith(t, err, refusal.CodeInvalid)
	if !strings.Contains(refused.Message, "p1") {
		t.Errorf("the refusal does not name the promotion that holds the tag: %s", refused.Message)
	}
}

func TestAPromoteRefusesAPromotionThePointerAlreadyRecords(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"))

	_, _, err := pointer.Promote(promotion("p1"), "p2", ledger.KeptPromotions)
	refusedWith(t, err, refusal.CodeInvalid)
}

func unpromoted(t *testing.T, pointer ledger.Pointer, promotionIDs ...string) ledger.Pointer {
	t.Helper()
	for _, id := range promotionIDs {
		var err error
		if pointer, err = pointer.Unpromote(id); err != nil {
			t.Fatalf("Unpromote(%s) = %v", id, err)
		}
	}
	return pointer
}

func TestUnpromotingPutsThePointerBackOnThePromotionItDisplacedAndKeepsItRecorded(t *testing.T) {
	t.Parallel()

	pointer := unpromoted(t, promoted(t, promotion("p1"), promotion("p2")), "p2")

	if pointer.Active != "p1" {
		t.Errorf("Active = %q, want p1: a router that could not serve p2 still serves p1", pointer.Active)
	}
	if got := ids(pointer.Entries); got != "p2,p1" {
		t.Fatalf("entries %s, want p2 still recorded beside p1", got)
	}
	if !pointer.Entries[0].Unpromoted || pointer.Entries[1].Unpromoted {
		t.Errorf("Unpromoted = %v, %v, want only p2 marked", pointer.Entries[0].Unpromoted, pointer.Entries[1].Unpromoted)
	}
}

func TestUnpromotingTheFirstPromotionLeavesThePointerAtNothing(t *testing.T) {
	t.Parallel()

	pointer := unpromoted(t, promoted(t, promotion("p1")), "p1")

	if pointer.Active != "" {
		t.Errorf("Active = %q, want nothing: no router serves anything under it", pointer.Active)
	}
}

func TestUnpromotingFollowsWhatWasDisplacedPastPromotionsAlreadyTakenBack(t *testing.T) {
	t.Parallel()

	pointer := unpromoted(t, promoted(t, promotion("p1"), promotion("p2"), promotion("p3")), "p2")
	if pointer.Active != "p3" {
		t.Fatalf("Active = %q, want p3: taking p2 back must not undo the promotion that followed it", pointer.Active)
	}

	pointer = unpromoted(t, pointer, "p3")
	if pointer.Active != "p1" {
		t.Errorf("Active = %q, want p1: p2 was taken back first, so no router ever served it", pointer.Active)
	}
}

func TestATagOnAPromotionTakenBackIsFreeForTheDeployThatRetriesIt(t *testing.T) {
	t.Parallel()

	tagged := promotion("p1")
	tagged.Tag = "v1"
	pointer := unpromoted(t, promoted(t, tagged), "p1")

	retried := promotion("p2")
	retried.Tag = "v1"
	after, _, err := pointer.Promote(retried, "", ledger.KeptPromotions)
	if err != nil {
		t.Fatalf("a retried deploy tagged v1 = %v, want the tag free: p1 never served", err)
	}
	for _, entry := range after.Entries {
		if entry.Tag == "v1" && entry.PromotionID != "p2" {
			t.Errorf("%s is still tagged v1 beside p2, so `ocel rollback --tag v1` names two releases", entry.PromotionID)
		}
	}
}

func TestAPromoteKeepsThePromotionItDisplacedHoweverFarDownItHasFallen(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"))
	for _, id := range []string{"p2", "p3", "p4"} {
		next, _, err := pointer.Promote(promotion(id), pointer.Active, ledger.KeptPromotions)
		if err != nil {
			t.Fatal(err)
		}
		pointer = unpromoted(t, next, id)
	}
	after, dropped, err := pointer.Promote(promotion("p5"), "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(after.Entries); got != "p5,p4,p1" {
		t.Errorf("kept %s, want p5,p4 and p1: p1 still serves until p5 flips, and taking p5 back returns to it", got)
	}
	if got := ids(dropped); got != "p3,p2" {
		t.Errorf("dropped %s, want p3,p2", got)
	}
}

func TestHistoryReadsNewestFirstAndMarksOnlyTheActivePromotion(t *testing.T) {
	t.Parallel()

	history := unpromoted(t, promoted(t, promotion("p1"), promotion("p2"), promotion("p3")), "p3").History()

	var order []string
	for _, entry := range history {
		order = append(order, entry.PromotionID)
	}
	if strings.Join(order, ",") != "p3,p2,p1" {
		t.Fatalf("history order = %v, want p3,p2,p1", order)
	}
	if history[0].Active || !history[1].Active || history[2].Active {
		t.Errorf("history = %+v, want only p2, the promotion p3 was taken back to, active", history)
	}
}

func TestUnpromotingAPromotionThePointerNeverRecordedIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := promoted(t, promotion("p1")).Unpromote("p9"); err == nil {
		t.Error("Unpromote(p9) = nil, want an error naming a promotion the pointer does not record")
	}
}
