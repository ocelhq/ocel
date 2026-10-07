package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type store struct {
	rows         map[string]keyvalue.Entry
	rev          int
	reads        map[string]int
	writes       map[string]int
	beforeWrite  func(key string)
	beforeList   func()
	beforeRemove func()
}

func newStore() *store {
	return &store{rows: map[string]keyvalue.Entry{}, reads: map[string]int{}, writes: map[string]int{}}
}

func (s *store) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := ctx.Err(); err != nil {
		return keyvalue.Entry{}, err
	}
	s.reads[key.String()]++
	recorded, ok := s.rows[key.String()]
	if !ok {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return recorded, nil
}

func (s *store) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := keyvalue.RefuseUnwritable(entry); err != nil {
		return "", err
	}
	if before := s.beforeWrite; before != nil {
		s.beforeWrite = nil
		before(entry.Key.String())
	}
	s.writes[entry.Key.String()]++
	if recorded := s.rows[entry.Key.String()]; recorded.Revision != entry.Revision {
		return "", keyvalue.ErrStale
	}
	s.rev++
	entry.Revision = keyvalue.Revision(strconv.Itoa(s.rev))
	s.rows[entry.Key.String()] = entry
	return entry.Revision, nil
}

func (s *store) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	if recorded := s.rows[second.Key.String()]; recorded.Revision != second.Revision {
		return keyvalue.ErrStale
	}
	if _, err := s.Write(ctx, first); err != nil {
		return err
	}
	_, err := s.Write(ctx, second)
	return err
}

func (s *store) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if before := s.beforeRemove; before != nil {
		s.beforeRemove = nil
		before()
	}
	recorded, ok := s.rows[key.String()]
	if !ok {
		return keyvalue.ErrNotFound
	}
	if recorded.Revision != expected {
		return keyvalue.ErrStale
	}
	delete(s.rows, key.String())
	return nil
}

func (s *store) List(_ context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if before := s.beforeList; before != nil {
		s.beforeList = nil
		before()
	}
	var out []keyvalue.Entry
	for _, entry := range s.rows {
		if entry.Key.Partition.String() != in.String() || len(entry.Key.Path) < len(under) || !slices.Equal(entry.Key.Path[:len(under)], under) {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *store) forget() {
	s.reads, s.writes = map[string]int{}, map[string]int{}
}

func fixture() (*Ledger, *store) {
	store := newStore()
	return New(store, environment.TierProduction, "shop"), store
}

func staged(t *testing.T, l *Ledger, id string) router.Promotion {
	t.Helper()
	if err := l.PutStaged(context.Background(), router.ReleaseRecord{App: "web", Release: "web-" + id}); err != nil {
		t.Fatal(err)
	}
	return router.Promotion{PromotionID: id, Releases: map[string]string{"web": "web-" + id}}
}

func promoting(t *testing.T, l *Ledger, pointer string, promotionIDs ...string) {
	t.Helper()
	for _, id := range promotionIDs {
		replaces, err := l.ActivePromotionID(context.Background(), pointer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Promote(context.Background(), staged(t, l, id), pointer, replaces); err != nil {
			t.Fatalf("Promote(%s) = %v", id, err)
		}
	}
}

func activeIn(t *testing.T, l *Ledger, pointer string) string {
	t.Helper()
	active, err := l.ActivePromotionID(context.Background(), pointer)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func TestAPromoteIsOneReadAndOneWriteOfThePointerDocument(t *testing.T) {
	l, store := fixture()
	promoting(t, l, "", "p1")
	next := staged(t, l, "p2")
	store.forget()

	if _, err := l.Promote(context.Background(), next, "", "p1"); err != nil {
		t.Fatal(err)
	}

	pointer := l.pointerKey(router.DefaultPointer).String()
	if store.reads[pointer] != 1 || store.writes[pointer] != 1 {
		t.Errorf("a promote read the pointer document %d times and wrote it %d times, want once each", store.reads[pointer], store.writes[pointer])
	}
	for key, n := range store.reads {
		if key != pointer {
			t.Errorf("a promote that dropped nothing read %s %d times, want only the pointer document", key, n)
		}
	}
	for key, n := range store.writes {
		if key != pointer {
			t.Errorf("a promote that dropped nothing wrote %s %d times, want only the pointer document", key, n)
		}
	}
}

func TestAPromoteThatLosesTheWriteToARacingPromoteIsRefusedBusy(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1")
	racer := staged(t, l, "p9")
	loser := staged(t, l, "p2")
	store.beforeWrite = func(string) {
		if _, err := l.Promote(ctx, racer, "", "p1"); err != nil {
			t.Fatalf("the racing Promote(p9) = %v", err)
		}
	}

	_, err := l.Promote(ctx, loser, "", "p1")

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("a promote the pointer moved under = %v, want a busy refusal", err)
	}
	if active := activeIn(t, l, ""); active != "p9" {
		t.Errorf("the pointer names %q, want p9, the promote that won", active)
	}
	history, err := l.History(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range history {
		if entry.PromotionID == "p2" {
			t.Error("the history records p2, the promote that was refused")
		}
	}
}

func TestAPromoteThatLosesTheWriteToAChangeThatLeftWhatItReplacesActiveLands(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1")
	next := staged(t, l, "p2")
	pointer := l.pointerKey(router.DefaultPointer)
	store.beforeWrite = func(string) {
		recorded := store.rows[pointer.String()]
		if _, err := store.Write(ctx, recorded); err != nil {
			t.Fatalf("rewrite the pointer document: %v", err)
		}
	}

	if _, err := l.Promote(ctx, next, "", "p1"); err != nil {
		t.Fatalf("a promote whose pointer was rewritten with p1 still active = %v, want it to land", err)
	}
	if active := activeIn(t, l, ""); active != "p2" {
		t.Errorf("the pointer names %q, want p2", active)
	}
}

func promotedPastTheKept(t *testing.T, l *Ledger) []RecordedPromotion {
	t.Helper()
	for i := range KeptPromotions {
		promoting(t, l, "", fmt.Sprintf("p%02d", i))
	}
	dropped, err := l.Promote(context.Background(), staged(t, l, "latest"), "", activeIn(t, l, ""))
	if err != nil {
		t.Fatal(err)
	}
	return dropped
}

func TestAPromoteThatDropsPromotionsIsStillOneReadAndOneWriteOfThePointerDocument(t *testing.T) {
	l, store := fixture()
	for i := range KeptPromotions {
		promoting(t, l, "", fmt.Sprintf("p%02d", i))
	}
	next := staged(t, l, "latest")
	store.forget()

	dropped, err := l.Promote(context.Background(), next, "", activeIn(t, l, ""))
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(collectPromotionIDs(dropped), ","); got != "p00" {
		t.Errorf("dropped %s, want p00: a promote keeps the newest %d", got, KeptPromotions)
	}
	pointer := l.pointerKey(router.DefaultPointer).String()
	for key, n := range store.writes {
		if key != pointer {
			t.Errorf("a promote that dropped p00 wrote %s %d times, want only the pointer document: once it has landed, nothing left for it to do can fail", key, n)
		}
	}
}

func TestTheRecordsOfTheBuildsAPromoteDroppedStayUntilTheyAreForgotten(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	dropped := promotedPastTheKept(t, l)

	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || !found {
		t.Fatalf("the record of the dropped build = found %v, %v, want it kept until its stack is reclaimed", found, err)
	}
	unnamed, err := l.ReadUnnamedRecords(ctx, "", dropped)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{RecordKey("web", "web-p00")}; !slices.Equal(unnamed.UnnamedRecordKeys, want) {
		t.Errorf("unnamed records %v, want %v", unnamed.UnnamedRecordKeys, want)
	}
	if want := "p00"; strings.Join(unnamed.RemovedPromotionIDs, ",") != want {
		t.Errorf("removed promotions %v, want %s", unnamed.RemovedPromotionIDs, want)
	}
	if err := l.ForgetUnnamedRecords(ctx, unnamed.UnnamedRecordKeys); err != nil {
		t.Fatal(err)
	}
	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || found {
		t.Errorf("the record of the dropped build once forgotten = found %v, %v, want it removed", found, err)
	}
	if _, found, err := l.Record(ctx, "web", "web-p01"); err != nil || !found {
		t.Errorf("the record of a kept build = found %v, %v, want it kept", found, err)
	}
}

func TestForgettingARecordAnotherPointerNamedSinceTheReclaimReadItKeepsIt(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	dropped := promotedPastTheKept(t, l)
	unnamed, err := l.ReadUnnamedRecords(ctx, "", dropped)
	if err != nil {
		t.Fatal(err)
	}
	store.beforeList = func() {
		shared := router.Promotion{PromotionID: "s1", Releases: map[string]string{"web": "web-p00"}}
		if _, err := l.Promote(ctx, shared, "staging", ""); err != nil {
			t.Fatalf("the promote on staging = %v", err)
		}
	}

	if err := l.ForgetUnnamedRecords(ctx, unnamed.UnnamedRecordKeys); err != nil {
		t.Fatal(err)
	}
	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || !found {
		t.Errorf("the record staging promoted during the reclaim = found %v, %v, want it kept", found, err)
	}
}

func TestAPromoteOfARecordRestagedWhileAReclaimRemovedItIsRefusedBusy(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	dropped := promotedPastTheKept(t, l)
	unnamed, err := l.ReadUnnamedRecords(ctx, "", dropped)
	if err != nil {
		t.Fatal(err)
	}
	store.beforeList = func() {
		if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "web-p00"}); err != nil {
			t.Fatalf("restage web-p00 = %v", err)
		}
	}
	if err := l.ForgetUnnamedRecords(ctx, unnamed.UnnamedRecordKeys); err != nil {
		t.Fatal(err)
	}
	restaged := router.Promotion{PromotionID: "s1", Releases: map[string]string{"web": "web-p00"}}
	if _, err := l.Promote(ctx, restaged, "staging", ""); err != nil {
		t.Fatal(err)
	}

	err = l.RewriteRecords(ctx, restaged)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("RewriteRecords() of a record the reclaim removed after its restage = %v, want a busy refusal before any router moves a pointer", err)
	}
}

func TestARecordAPromoteRewroteAfterTheReclaimReadThePointersIsKept(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	dropped := promotedPastTheKept(t, l)
	unnamed, err := l.ReadUnnamedRecords(ctx, "", dropped)
	if err != nil {
		t.Fatal(err)
	}
	store.beforeRemove = func() {
		shared := router.Promotion{PromotionID: "s1", Releases: map[string]string{"web": "web-p00"}}
		if _, err := l.Promote(ctx, shared, "staging", ""); err != nil {
			t.Fatalf("the promote on staging = %v", err)
		}
		if err := l.RewriteRecords(ctx, shared); err != nil {
			t.Fatalf("RewriteRecords(s1) = %v", err)
		}
	}

	if err := l.ForgetUnnamedRecords(ctx, unnamed.UnnamedRecordKeys); err != nil {
		t.Fatal(err)
	}
	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || !found {
		t.Errorf("the record staging promoted once the reclaim had read the pointers = found %v, %v, want it kept", found, err)
	}
}

func TestRewritingARecordAReclaimRemovedIsRefusedBusy(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promotion := staged(t, l, "p1")
	if err := keyvalue.Forget(ctx, l.keyValues, l.releaseKey("web", "web-p1")); err != nil {
		t.Fatal(err)
	}

	err := l.RewriteRecords(ctx, promotion)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy || !strings.Contains(refused.Message, "web-p1") {
		t.Fatalf("RewriteRecords over a record a reclaim removed = %v, want a busy refusal naming web-p1", err)
	}
}

func TestADroppedBuildAnotherPointerStillNamesIsNotUnnamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "staging", "p00")
	shared := router.Promotion{PromotionID: "s1", Releases: map[string]string{"web": "web-p00"}}
	if _, err := l.Promote(ctx, shared, "", ""); err != nil {
		t.Fatal(err)
	}
	var dropped []RecordedPromotion
	for i := 1; i <= KeptPromotions; i++ {
		replaces := activeIn(t, l, "staging")
		lost, err := l.Promote(ctx, staged(t, l, fmt.Sprintf("p%02d", i)), "staging", replaces)
		if err != nil {
			t.Fatal(err)
		}
		dropped = append(dropped, lost...)
	}

	unnamed, err := l.ReadUnnamedRecords(ctx, "staging", dropped)
	if err != nil {
		t.Fatal(err)
	}
	if len(unnamed.UnnamedRecordKeys) != 0 {
		t.Errorf("unnamed records %v, want none: @production still names web-p00", unnamed.UnnamedRecordKeys)
	}
}

func TestRemovingAPointerKeepsTheRecordsAnotherPointerStillNames(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "pr-7", "p1")
	shared := router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": "web-p1"}}
	if _, err := l.Promote(ctx, shared, "pr-8", ""); err != nil {
		t.Fatal(err)
	}

	removed, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.UnnamedRecordKeys) != 0 {
		t.Errorf("unnamed records %v, want none: pr-8 still serves web-p1", removed.UnnamedRecordKeys)
	}
	if _, found, err := l.Record(ctx, "web", "web-p1"); err != nil || !found {
		t.Errorf("the record pr-8 serves = found %v, %v, want it kept", found, err)
	}
}

func TestRemovingAPointerRemovesItAndNamesTheRecordsOnlyItNamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "pr-7", "p1", "p2")

	removed, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(removed.UnnamedRecordKeys, ","); got != "record:web/web-p1,record:web/web-p2" {
		t.Errorf("unnamed records %s, want both of pr-7's", got)
	}
	if got := strings.Join(removed.RemovedPromotionIDs, ","); got != "p2,p1" {
		t.Errorf("removed promotions %s, want p2,p1", got)
	}
	pointers, err := l.Pointers(ctx)
	if err != nil || len(pointers) != 0 {
		t.Errorf("Pointers() = %v, %v, want none left", pointers, err)
	}
}

func TestHistoryAndTheActivePromotionAreOneReadEach(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1", "p2")
	pointer := l.pointerKey(router.DefaultPointer).String()

	for name, read := range map[string]func() error{
		"History":           func() error { _, err := l.History(ctx, ""); return err },
		"ActivePromotionID": func() error { _, err := l.ActivePromotionID(ctx, ""); return err },
		"ReadActive":        func() error { _, _, err := l.ReadActive(ctx, ""); return err },
	} {
		store.forget()
		if err := read(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(store.reads) != 1 || store.reads[pointer] != 1 {
			t.Errorf("%s read %v, want the pointer document once", name, store.reads)
		}
	}
}

func TestReadActiveReadsThePromotionThePointerNamesAndNothingOnceItNamesNone(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1", "p2")

	active, found, err := l.ReadActive(ctx, "")
	if err != nil || !found || active.PromotionID != "p2" || active.Releases["web"] != "web-p2" {
		t.Fatalf("ReadActive = %+v, %v, %v, want p2 and the build it promoted", active, found, err)
	}
	for _, id := range []string{"p2", "p1"} {
		if err := l.Unpromote(ctx, id, ""); err != nil {
			t.Fatalf("Unpromote(%s) = %v", id, err)
		}
	}
	if active, found, err := l.ReadActive(ctx, ""); err != nil || found {
		t.Errorf("ReadActive once every promotion was taken back = %+v, %v, %v, want nothing", active, found, err)
	}
}

func TestUnpromotingUnderAnInterruptedDeployStillPutsThePointerBack(t *testing.T) {
	l, _ := fixture()
	promoting(t, l, "", "p1", "p2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := l.Unpromote(ctx, "p2", ""); err != nil {
		t.Fatalf("Unpromote(p2) under a cancelled context = %v: an interrupted deploy is exactly the one whose routers never served its promotion", err)
	}
	if active := activeIn(t, l, ""); active != "p1" {
		t.Errorf("the pointer names %q, want p1", active)
	}
}

func TestPruneNamesTheDeploymentPointerAndHostsOfEachPromotionItDropped(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	for _, id := range []string{"p1", "p2", "p3"} {
		promotion := staged(t, l, id)
		promotion.Hosts = []edge.PreviewHost{{Hostname: "pr-7-" + id + ".preview.acme.com", App: "web"}}
		replaces := activeIn(t, l, "pr-7")
		if _, err := l.Promote(ctx, promotion, "pr-7", replaces); err != nil {
			t.Fatalf("Promote(%s) = %v", id, err)
		}
	}

	result, err := l.Prune(ctx, 2, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	want := []router.PointerRemoval{{Pointer: "pr-7@p1", Hosts: []edge.PreviewHost{{Hostname: "pr-7-p1.preview.acme.com", App: "web"}}}}
	if !reflect.DeepEqual(result.ReleaseRemovals, want) {
		t.Errorf("removals = %+v, want %+v: a pruned deployment stops serving on its own hostnames", result.ReleaseRemovals, want)
	}

	removed, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, removal := range removed.ReleaseRemovals {
		got = append(got, removal.Pointer)
	}
	if want := []string{"pr-7@p3", "pr-7@p2", "pr-7@p1"}; !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v: removing a preview removes every deployment it kept and every one whose withdrawal is still pending", got, want)
	}
}

func TestADroppedDeploymentIsNamedByEveryPruneUntilItsWithdrawalIsForgotten(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		promotion := staged(t, l, id)
		promotion.Hosts = []edge.PreviewHost{{Hostname: "pr-7-" + id + ".preview.acme.com", App: "web"}}
		if _, err := l.Promote(ctx, promotion, "pr-7", activeIn(t, l, "pr-7")); err != nil {
			t.Fatalf("Promote(%s) = %v", id, err)
		}
	}
	pointers := func(removals []router.PointerRemoval) []string {
		var named []string
		for _, removal := range removals {
			named = append(named, removal.Pointer)
		}
		return named
	}

	if _, err := l.Prune(ctx, 3, "pr-7"); err != nil {
		t.Fatal(err)
	}
	again, err := l.Prune(ctx, 2, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pointers(again.ReleaseRemovals), []string{"pr-7@p2", "pr-7@p1"}; !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v: p1's withdrawal was never confirmed, so it is retried", got, want)
	}

	if err := l.ForgetPendingRemovals(ctx, "pr-7", []string{"pr-7@p1", "pr-7@p2"}); err != nil {
		t.Fatal(err)
	}
	read, err := l.Read(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if got := pointers(read.ListReleaseRemovals()); !slices.Equal(got, []string{"pr-7@p4", "pr-7@p3"}) {
		t.Errorf("removals = %v, want only the kept p4 and p3 once the withdrawals are forgotten", got)
	}
}

func TestPruneKeepsNAndTheActivePromotionAndNamesTheRestsRecords(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1", "p2", "p3", "p4")

	result, err := l.Prune(ctx, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.KeptPromotionIDs, ","); got != "p4,p3" {
		t.Errorf("kept = %s", got)
	}
	if got := strings.Join(result.RemovedPromotionIDs, ","); got != "p2,p1" {
		t.Errorf("removed = %s", got)
	}
	if got := strings.Join(result.UnnamedRecordKeys, ","); got != "record:web/web-p1,record:web/web-p2" {
		t.Errorf("unnamed record keys = %s", got)
	}
	if got := strings.Join(result.SurvivingRecordKeys, ","); got != "record:web/web-p3,record:web/web-p4" {
		t.Errorf("surviving record keys = %s", got)
	}
	if got := strings.Join(result.SurvivingPointerRecordKeys, ","); got != "record:web/web-p3,record:web/web-p4" {
		t.Errorf("surviving pointer record keys = %s", got)
	}
	history, err := l.History(ctx, "")
	if err != nil || len(history) != 2 {
		t.Errorf("history after prune = %d entries, %v", len(history), err)
	}
}

func TestPruneKeepsARecordAKeptPromotionStillNames(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1")
	again := router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": "web-p1"}}
	if _, err := l.Promote(ctx, again, "", "p1"); err != nil {
		t.Fatal(err)
	}

	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UnnamedRecordKeys) != 0 {
		t.Errorf("unnamed record keys = %v, want nothing: the kept promotion serves the same build", result.UnnamedRecordKeys)
	}
	if _, found, err := l.Record(ctx, "web", "web-p1"); err != nil || !found {
		t.Errorf("the record the kept promotion serves = found %v, %v, want it kept", found, err)
	}
}

func TestATagIsFreedWithThePromotionItNamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	tagged := staged(t, l, "p1")
	tagged.Tag = "live"
	if _, err := l.Promote(ctx, tagged, "", ""); err != nil {
		t.Fatal(err)
	}
	promoting(t, l, "", "p2", "p3")
	if _, err := l.Prune(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}

	retagged := staged(t, l, "p4")
	retagged.Tag = "live"
	if _, err := l.Promote(ctx, retagged, "", "p3"); err != nil {
		t.Errorf("a promote tagged with the tag of a pruned promotion = %v, want the tag free", err)
	}
}

func TestPointersAndDestroy(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	promoting(t, l, "", "p1")
	promoting(t, l, "staging", "p2")

	pointers, err := l.Pointers(ctx)
	if err != nil || strings.Join(pointers, ",") != router.DefaultPointer+",staging" {
		t.Fatalf("Pointers() = %v, %v", pointers, err)
	}
	if err := l.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 0 {
		t.Fatalf("Destroy() left %d entries behind", len(store.rows))
	}
}

func TestThePointerDocumentIsJSONACustomerCanRead(t *testing.T) {
	l, store := fixture()
	promoting(t, l, "", "p1")

	var document struct {
		Name       string `json:"name"`
		Active     string `json:"active"`
		Promotions []struct {
			PromotionID string `json:"promotionId"`
		} `json:"promotions"`
	}
	if err := json.Unmarshal(store.rows[l.pointerKey(router.DefaultPointer).String()].Value, &document); err != nil {
		t.Fatal(err)
	}
	if document.Name != router.DefaultPointer || document.Active != "p1" || len(document.Promotions) != 1 || document.Promotions[0].PromotionID != "p1" {
		t.Errorf("the pointer document reads %+v, want @production naming p1", document)
	}
}

func TestRestagingABuildWithTheRecordItHoldsSucceeds(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	record := router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://first.invalid", RoutingManifest: json.RawMessage(`{ "routes": [] }`)}
	if err := l.PutStaged(ctx, record); err != nil {
		t.Fatal(err)
	}

	if err := l.PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged() of the record the build holds = %v, want it to succeed: it is the same write repeated", err)
	}
}

func TestRestagingABuildWithAnotherRecordIsRefusedAndKeepsTheFirst(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://first.invalid"}); err != nil {
		t.Fatal(err)
	}

	err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://second.invalid"})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "b1") {
		t.Fatalf("PutStaged() of another record for b1 = %v, want a refusal naming the build", err)
	}
	if got, _, _ := l.Record(ctx, "web", "b1"); got.Origin != "https://first.invalid" {
		t.Errorf("after the refused restage b1 names origin %q, want the first record's", got.Origin)
	}
}

func TestAStageThatLosesTheCreateToAnotherRecordIsRefused(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	store.beforeWrite = func(string) {
		if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://first.invalid"}); err != nil {
			t.Fatalf("the stage that won = %v", err)
		}
	}

	err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://second.invalid"})

	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("PutStaged() that lost the create to another record = %v, want a refusal", err)
	}
	if got, _, _ := l.Record(ctx, "web", "b1"); got.Origin != "https://first.invalid" {
		t.Errorf("b1 names origin %q, want the record that won the create", got.Origin)
	}
}

func TestAStageThatLosesTheCreateToTheSameRecordSucceeds(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()
	record := router.ReleaseRecord{App: "web", Release: "b1", Origin: "https://first.invalid"}
	store.beforeWrite = func(string) {
		if err := l.PutStaged(ctx, record); err != nil {
			t.Fatalf("the stage that won = %v", err)
		}
	}

	if err := l.PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged() that lost the create to the same record = %v, want it to succeed", err)
	}
}

func TestStagedRecordsRoundTrip(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, found, err := l.Record(ctx, "web", "abc"); err != nil || found {
		t.Fatalf("Record() before staging = %v, %v, want nothing found", found, err)
	}
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "abc"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.Record(ctx, "web", "abc")
	if err != nil || !found || got.App != "web" || got.Release != "abc" {
		t.Fatalf("Record() = %+v, %v, %v", got, found, err)
	}
	if err := l.PutStaged(ctx, router.ReleaseRecord{App: "web"}); err == nil {
		t.Fatal("PutStaged() with no release succeeded, want it refused")
	}
}

func TestStateRecordedUnderTheOldBuildsAndIdentityKeysIsNotReadBack(t *testing.T) {
	l, store := fixture()
	ctx := context.Background()

	store.rows[l.pointerKey("@production").String()] = keyvalue.Entry{
		Key:      l.pointerKey("@production"),
		Value:    []byte(`{"name":"@production","active":"p1","sequence":1,"promotions":[{"promotionId":"p1","ts":1,"builds":{"web":"abc~fp"},"sequence":1}]}`),
		Revision: "1",
	}
	store.rows[l.releaseKey("web", "abc~fp").String()] = keyvalue.Entry{
		Key:      l.releaseKey("web", "abc~fp"),
		Value:    []byte(`{"app":"web","identity":"abc~fp","deploymentId":"abc"}`),
		Revision: "2",
	}

	pointer, err := l.Read(ctx, "")
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(pointer.Promotions) != 1 || len(pointer.Promotions[0].Releases) != 0 {
		t.Errorf("Read() = %+v, want the promotion the old shape recorded to name no release: the old key is not read back", pointer.Promotions)
	}
	record, found, err := l.Record(ctx, "web", "abc~fp")
	if err != nil || !found {
		t.Fatalf("Record() = %v, %v, want the row found", found, err)
	}
	if record.Release != "" || record.BuildID != "" {
		t.Errorf("Record() = %+v, want no release or build id from the old identity and deploymentId keys", record)
	}
}
