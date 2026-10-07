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
	return router.Promotion{PromotionID: id, Releases: map[string]string{"web": "web-" + id}}
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
	if len(pointer.Promotions) != 2 || pointer.Promotions[0].PromotionID != "p2" || pointer.Promotions[1].PromotionID != "p1" {
		t.Fatalf("Promotions = %+v, want p2 then p1", pointer.Promotions)
	}
	if pointer.Promotions[0].Displaced != "p1" || pointer.Promotions[1].Displaced != "" {
		t.Errorf("Displaced = %q, %q, want p1 and nothing", pointer.Promotions[0].Displaced, pointer.Promotions[1].Displaced)
	}
	if pointer.Promotions[0].Sequence != 2 || pointer.Promotions[1].Sequence != 1 || pointer.Sequence != 2 {
		t.Errorf("Sequence = %d, %d on a pointer at %d, want 2, 1 on a pointer at 2", pointer.Promotions[0].Sequence, pointer.Promotions[1].Sequence, pointer.Sequence)
	}
}

func TestAPromoteOverAPromotionThePointerNoLongerNamesIsRefusedBusy(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"))

	after, _, err := pointer.Promote(promotion("p3"), "p1", ledger.KeptPromotions)
	refusedWith(t, err, refusal.CodeBusy)
	if after.Active != "" || len(after.Promotions) != 0 {
		t.Errorf("a refused promote returned %+v, want nothing", after)
	}
	if pointer.Active != "p2" || len(pointer.Promotions) != 2 {
		t.Errorf("a refused promote changed the pointer it was asked of: %+v", pointer)
	}
}

func ids(promotions []ledger.RecordedPromotion) string {
	named := make([]string, 0, len(promotions))
	for _, recorded := range promotions {
		named = append(named, recorded.PromotionID)
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
	if got := ids(after.Promotions); got != "p4,p3" {
		t.Errorf("kept %s, want p4,p3", got)
	}
	if got := ids(dropped); got != "p2,p1" {
		t.Errorf("dropped %s, want p2,p1", got)
	}
}

func recorded(active string, promotionIDs ...string) ledger.Pointer {
	pointer := ledger.Pointer{Name: router.DefaultPointer, Active: active}
	for _, id := range promotionIDs {
		pointer.Promotions = append(pointer.Promotions, ledger.RecordedPromotion{Promotion: promotion(id)})
	}
	return pointer
}

func TestRetainNeverDropsWhatIsServingNow(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("p2", "p4", "p3", "p2", "p1").Retain(1)
	if got := ids(kept.Promotions); got != "p4,p2" {
		t.Errorf("kept %s, want p4 and the promotion the pointer serves however old it is", got)
	}
	if got := ids(dropped); got != "p3,p1" {
		t.Errorf("dropped %s, want p3,p1", got)
	}
}

func TestRetainKeepingNoneStillKeepsWhatIsServing(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("p1", "p2", "p1").Retain(0)
	if got := ids(kept.Promotions); got != "p1" {
		t.Errorf("kept %s, want only the active promotion", got)
	}
	if got := ids(dropped); got != "p2" {
		t.Errorf("dropped %s, want p2", got)
	}
}

func TestRetainOverNothingDropsNothing(t *testing.T) {
	t.Parallel()

	kept, dropped := recorded("").Retain(3)
	if len(kept.Promotions) != 0 || len(dropped) != 0 {
		t.Errorf("Retain() over an empty pointer = %+v / %+v, want nothing either way", kept.Promotions, dropped)
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
	if got := ids(pointer.Promotions); got != "p2,p1" {
		t.Fatalf("promotions %s, want p2 still recorded beside p1", got)
	}
	if !pointer.Promotions[0].Unpromoted || pointer.Promotions[1].Unpromoted {
		t.Errorf("Unpromoted = %v, %v, want only p2 marked", pointer.Promotions[0].Unpromoted, pointer.Promotions[1].Unpromoted)
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
	for _, recorded := range after.Promotions {
		if recorded.Tag == "v1" && recorded.PromotionID != "p2" {
			t.Errorf("%s is still tagged v1 beside p2, so `ocel rollback --tag v1` names two releases", recorded.PromotionID)
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
	if got := ids(after.Promotions); got != "p5,p4,p1" {
		t.Errorf("kept %s, want p5,p4 and p1: p1 still serves until the pointer moves to p5, and taking p5 back returns to it", got)
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
	if !history[0].Unpromoted || history[1].Unpromoted || history[2].Unpromoted {
		t.Errorf("history = %+v, want only p3 marked as taken back", history)
	}
}

func TestARollbackPromotesTheTargetsBuildsAsANewPromotion(t *testing.T) {
	t.Parallel()

	pointer := promoted(t, promotion("p1"), promotion("p2"))
	rolled := router.Promotion{PromotionID: "r1", Releases: pointer.Promotions[1].Releases}

	after, _, err := pointer.Rollback("p1", rolled, "p2", ledger.KeptPromotions)
	if err != nil {
		t.Fatalf("Rollback(p1) = %v", err)
	}
	if after.Active != "r1" || ids(after.Promotions) != "r1,p2,p1" {
		t.Errorf("after the rollback the pointer is at %q with %s, want r1 active beside p2 and p1", after.Active, ids(after.Promotions))
	}
}

func TestARollbackToAPromotionTakenBackIsRefused(t *testing.T) {
	t.Parallel()

	pointer := unpromoted(t, promoted(t, promotion("p1"), promotion("p2"), promotion("p3")), "p2")

	_, _, err := pointer.Rollback("p2", router.Promotion{PromotionID: "r1", Releases: promotion("p2").Releases}, "p3", ledger.KeptPromotions)
	refused := refusedWith(t, err, refusal.CodeInvalid)
	if !strings.Contains(refused.Message, "p2") {
		t.Errorf("the refusal does not name the promotion taken back: %s", refused.Message)
	}
}

func TestARollbackToAPromotionThePointerNoLongerRecordsIsRefused(t *testing.T) {
	t.Parallel()

	pointer, _ := promoted(t, promotion("p1"), promotion("p2"), promotion("p3")).Retain(0)

	_, _, err := pointer.Rollback("p1", router.Promotion{PromotionID: "r1", Releases: promotion("p1").Releases}, "p3", ledger.KeptPromotions)
	refusedWith(t, err, refusal.CodeInvalid)
}

func TestUnpromotingAPromotionThePointerNeverRecordedIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := promoted(t, promotion("p1")).Unpromote("p9"); err == nil {
		t.Error("Unpromote(p9) = nil, want an error naming a promotion the pointer does not record")
	}
}

func TestRetainKeepsWhatTakingTheActivePromotionBackWouldServe(t *testing.T) {
	t.Parallel()

	kept, dropped := promoted(t, promotion("p1"), promotion("p2"), promotion("p3")).Retain(0)
	if got := ids(kept.Promotions); got != "p3,p2" {
		t.Errorf("kept %s, want p3 and p2: a prune racing p3's pointer move must leave the promotion a failed pointer move falls back to", got)
	}
	if got := ids(dropped); got != "p1" {
		t.Errorf("dropped %s, want p1", got)
	}
	if after := unpromoted(t, kept, "p3"); after.Active != "p2" {
		t.Errorf("taking p3 back after the prune leaves the pointer at %q, want p2", after.Active)
	}
}

func TestRetainKeepsTheFallbackPastPromotionsAlreadyTakenBack(t *testing.T) {
	t.Parallel()

	pointer := unpromoted(t, promoted(t, promotion("p1"), promotion("p2"), promotion("p3")), "p2")
	kept, dropped := pointer.Retain(0)
	if got := ids(kept.Promotions); got != "p3,p2,p1" {
		t.Errorf("kept %s, want p3, p2 and p1: p2 was taken back, so taking p3 back falls back to p1", got)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped %s, want nothing", ids(dropped))
	}
	if after := unpromoted(t, kept, "p3"); after.Active != "p1" {
		t.Errorf("taking p3 back after the prune leaves the pointer at %q, want p1", after.Active)
	}
}
