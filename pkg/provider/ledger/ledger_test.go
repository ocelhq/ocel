package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type store struct {
	rows        map[string]keyvalue.Entry
	rev         int
	reads       map[string]int
	writes      map[string]int
	beforeWrite func(key string)
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
	if err := l.PutStaged(context.Background(), router.DeploymentRecord{App: "web", Build: "web-" + id}); err != nil {
		t.Fatal(err)
	}
	return router.Promotion{PromotionID: id, Builds: map[string]string{"web": "web-" + id}}
}

func promoting(t *testing.T, l *Ledger, pointer string, promotionIDs ...string) {
	t.Helper()
	for _, id := range promotionIDs {
		over, err := l.ActivePromotionID(context.Background(), pointer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Promote(context.Background(), staged(t, l, id), pointer, over); err != nil {
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

func TestAPromoteRemovesTheRecordsOfTheBuildsItDropped(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	for i := range KeptPromotions {
		promoting(t, l, "", fmt.Sprintf("p%02d", i))
	}
	over := activeIn(t, l, "")

	pruned, err := l.Promote(ctx, staged(t, l, "latest"), "", over)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"p00"}; !slices.Equal(pruned.RemovedPromotionIDs, want) {
		t.Errorf("dropped %v, want %v: a promote keeps the newest %d", pruned.RemovedPromotionIDs, want, KeptPromotions)
	}
	if want := []string{RecordKey("web", "web-p00")}; !slices.Equal(pruned.RemovedRecordKeys, want) {
		t.Errorf("removed records %v, want %v", pruned.RemovedRecordKeys, want)
	}
	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || found {
		t.Errorf("the record of the dropped build = found %v, %v, want it removed", found, err)
	}
	if _, found, err := l.Record(ctx, "web", "web-p01"); err != nil || !found {
		t.Errorf("the record of a kept build = found %v, %v, want it kept", found, err)
	}
}

func TestAPromoteKeepsTheRecordOfADroppedBuildAnotherPointerStillNames(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "staging", "p00")
	shared := router.Promotion{PromotionID: "s1", Builds: map[string]string{"web": "web-p00"}}
	if _, err := l.Promote(ctx, shared, "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= KeptPromotions; i++ {
		promoting(t, l, "staging", fmt.Sprintf("p%02d", i))
	}

	if _, found, err := l.Record(ctx, "web", "web-p00"); err != nil || !found {
		t.Errorf("the record %s dropped while @production still names it = found %v, %v, want it kept", "web-p00", found, err)
	}
}

func TestRemovingAPointerKeepsTheRecordsAnotherPointerStillNames(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "pr-7", "p1")
	shared := router.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "web-p1"}}
	if _, err := l.Promote(ctx, shared, "pr-8", ""); err != nil {
		t.Fatal(err)
	}

	removed, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.RemovedRecordKeys) != 0 {
		t.Errorf("removed records %v, want none: pr-8 still serves web-p1", removed.RemovedRecordKeys)
	}
	if _, found, err := l.Record(ctx, "web", "web-p1"); err != nil || !found {
		t.Errorf("the record pr-8 serves = found %v, %v, want it kept", found, err)
	}
}

func TestRemovingAPointerRemovesItAndTheRecordsOnlyItNamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, "pr-7", "p1", "p2")

	removed, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(removed.RemovedRecordKeys, ","); got != "record:web/web-p1,record:web/web-p2" {
		t.Errorf("removed records %s, want both of pr-7's", got)
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
	if err != nil || !found || active.PromotionID != "p2" || active.Builds["web"] != "web-p2" {
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

func TestPruneKeepsNAndTheActivePromotionAndRemovesTheRestsRecords(t *testing.T) {
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
	if got := strings.Join(result.RemovedRecordKeys, ","); got != "record:web/web-p1,record:web/web-p2" {
		t.Errorf("removed record keys = %s", got)
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
	again := router.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "web-p1"}}
	if _, err := l.Promote(ctx, again, "", "p1"); err != nil {
		t.Fatal(err)
	}

	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemovedRecordKeys) != 0 {
		t.Errorf("removed record keys = %v, want nothing: the kept promotion serves the same build", result.RemovedRecordKeys)
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
	promoting(t, l, "", "p2")
	if _, err := l.Prune(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}

	retagged := staged(t, l, "p3")
	retagged.Tag = "live"
	if _, err := l.Promote(ctx, retagged, "", "p2"); err != nil {
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
		Name    string `json:"name"`
		Active  string `json:"active"`
		Entries []struct {
			PromotionID string `json:"promotionId"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(store.rows[l.pointerKey(router.DefaultPointer).String()].Value, &document); err != nil {
		t.Fatal(err)
	}
	if document.Name != router.DefaultPointer || document.Active != "p1" || len(document.Entries) != 1 || document.Entries[0].PromotionID != "p1" {
		t.Errorf("the pointer document reads %+v, want @production naming p1", document)
	}
}

func TestAContainerBuildNamedByItsImageReferenceIsOneRecord(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	build := "ocel/web@sha256:0123"
	if err := l.PutStaged(ctx, router.DeploymentRecord{App: "web", Build: build}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := l.Record(ctx, "web", build); err != nil || !found || got.Build != build {
		t.Fatalf("Record(web, %s) = %+v, %v, %v, want the record staged", build, got, found, err)
	}
	if key := l.deploymentKey("web", build); len(key.Path) != 3 || key.Path[2] != build {
		t.Fatalf("the record for web at %s is keyed %s, want the image reference as one segment", build, key)
	}
}

func TestStagedRecordsRoundTrip(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, found, err := l.Record(ctx, "web", "abc"); err != nil || found {
		t.Fatalf("Record() before staging = %v, %v, want nothing found", found, err)
	}
	if err := l.PutStaged(ctx, router.DeploymentRecord{App: "web", Build: "abc"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.Record(ctx, "web", "abc")
	if err != nil || !found || got.App != "web" || got.Build != "abc" {
		t.Fatalf("Record() = %+v, %v, %v", got, found, err)
	}
	if err := l.PutStaged(ctx, router.DeploymentRecord{App: "web"}); err == nil {
		t.Fatal("PutStaged() with no build succeeded, want it refused")
	}
}
