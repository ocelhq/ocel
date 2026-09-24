package ledger

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type store struct {
	held   map[string]ports.Record
	rev    int
	racing func(name string)
}

func newStore() *store { return &store{held: map[string]ports.Record{}} }

func (s *store) Read(_ context.Context, name ports.RecordName) (ports.Record, error) {
	held, ok := s.held[name.String()]
	if !ok {
		return ports.Record{}, ports.ErrNoRecord
	}
	return held, nil
}

func (s *store) Write(_ context.Context, record ports.Record) (ports.Revision, error) {
	if s.racing != nil {
		s.racing(record.Name.String())
	}
	if held := s.held[record.Name.String()]; held.Revision != record.Revision {
		return "", ports.ErrStale
	}
	s.rev++
	record.Revision = ports.Revision(strconv.Itoa(s.rev))
	s.held[record.Name.String()] = record
	return record.Revision, nil
}

func (s *store) WritePair(ctx context.Context, first, second ports.Record) error {
	if held := s.held[second.Name.String()]; held.Revision != second.Revision {
		return ports.ErrStale
	}
	if _, err := s.Write(ctx, first); err != nil {
		return err
	}
	_, err := s.Write(ctx, second)
	return err
}

func (s *store) Remove(_ context.Context, name ports.RecordName, expected ports.Revision) error {
	held, ok := s.held[name.String()]
	if !ok {
		return ports.ErrNoRecord
	}
	if held.Revision != expected {
		return ports.ErrStale
	}
	delete(s.held, name.String())
	return nil
}

func (s *store) List(_ context.Context, under ports.RecordName) ([]ports.Record, error) {
	var out []ports.Record
	for key, record := range s.held {
		if strings.HasPrefix(key, under.String()+"/") {
			out = append(out, record)
		}
	}
	return out, nil
}

func fixture() (*Ledger, *store) {
	records := newStore()
	return New(records, ports.ClassProduction, "shop"), records
}

func TestNextSequenceRetriesPastAClaimerThatGotThereFirst(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()

	first, err := l.nextSequence(ctx)
	if err != nil || first != 1 {
		t.Fatalf("first sequence = %d, %v", first, err)
	}
	stale, err := ports.Held(ctx, records, l.sequenceName())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.nextSequence(ctx); err != nil {
		t.Fatal(err)
	}
	stale.Bytes = []byte("99")
	if _, err := records.Write(ctx, stale); !errors.Is(err, ports.ErrStale) {
		t.Fatalf("a write at a revision that moved = %v, want ErrStale", err)
	}
	third, err := l.nextSequence(ctx)
	if err != nil || third != 3 {
		t.Fatalf("third sequence = %d, %v", third, err)
	}
}

func TestClaimTagRefusesASecondClaimant(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatal(err)
	}
	_, err := l.claimTag(ctx, edge.Promotion{PromotionID: "p2", Tag: "live"})
	var refusal ports.Refusal
	if !errors.As(err, &refusal) || refusal.Code != ports.CodeInvalid {
		t.Fatalf("a second claim on the same tag = %v, want the tag refused", err)
	}
	if !strings.Contains(refusal.Message, "p1") {
		t.Fatalf("the refusal does not name the holder: %s", refusal.Message)
	}
}

func TestClaimTagLetsTheSamePromotionReclaimIt(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatalf("the holder reclaiming its own tag = %v, want it allowed", err)
	}
}

func TestPromoteRefusesAPointerAnotherDeployMoved(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1"}, "", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}
	pointer := l.pointerName(edge.DefaultPointer).String()
	records.racing = func(name string) {
		if name != pointer {
			return
		}
		records.racing = nil
		held := records.held[pointer]
		held.Revision = "another deploy got here"
		records.held[pointer] = held
	}

	err := l.Promote(ctx, edge.Promotion{PromotionID: "p2"}, "", edge.DiscardReporter())
	var refusal ports.Refusal
	if !errors.As(err, &refusal) || refusal.Code != ports.CodeBusy {
		t.Fatalf("promote onto a moved pointer = %v, want a busy refusal", err)
	}

	held, err := l.pointerAt(ctx, edge.DefaultPointer)
	if err != nil || held != "p1" {
		t.Fatalf("the pointer holds %q, %v, want the promotion the winner left there", held, err)
	}
	entries, err := l.History(ctx, "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("history = %d entries, %v, want the release the loser staged still recorded", len(entries), err)
	}
}

func TestHistoryOrdersNewestFirstAndMarksActive(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	for _, id := range []string{"p1", "p2", "p3"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, "", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := l.History(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, entry := range entries {
		order = append(order, entry.PromotionID)
	}
	if strings.Join(order, ",") != "p3,p2,p1" {
		t.Fatalf("history order = %v", order)
	}
	if !entries[0].Active || entries[1].Active || entries[2].Active {
		t.Fatalf("the pointer's promotion is not the only active one: %v", entries)
	}
}

func TestPruneKeepsNAndTheActivePromotion(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		if err := l.PutStaged(ctx, edge.DeploymentRecord{App: "web", Identity: id}); err != nil {
			t.Fatal(err)
		}
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id, Builds: map[string]string{"web": id}}, "", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := l.Prune(ctx, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.KeptPromotionIDs, ",") != "p4,p3" {
		t.Fatalf("kept = %v", result.KeptPromotionIDs)
	}
	if strings.Join(result.RemovedPromotionIDs, ",") != "p2,p1" {
		t.Fatalf("removed = %v", result.RemovedPromotionIDs)
	}
	if strings.Join(result.RemovedRecordKeys, ",") != "record:web/p1,record:web/p2" {
		t.Fatalf("removed record keys = %v", result.RemovedRecordKeys)
	}
	if strings.Join(result.SurvivingRecordKeys, ",") != "record:web/p3,record:web/p4" {
		t.Fatalf("surviving record keys = %v", result.SurvivingRecordKeys)
	}
	entries, err := l.History(ctx, "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("history after prune = %d entries, %v", len(entries), err)
	}
}

func TestPruneKeepsAnActivePromotionThatFellOutOfTheWindow(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "old"}, "", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p2", "p3", "p4"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, "staging", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := l.Prune(ctx, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.KeptPromotionIDs, ",") != "old" {
		t.Fatalf("kept = %v, want the promotion the pointer is on however far down it has fallen", result.KeptPromotionIDs)
	}
}

func TestPruneKeepsARecordAnUnprunedPromotionStillNames(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if err := l.PutStaged(ctx, edge.DeploymentRecord{App: "web", Identity: "b1"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p1", "p2"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id, Builds: map[string]string{"web": "b1"}}, "", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemovedRecordKeys) != 0 {
		t.Fatalf("removed record keys = %v, want nothing: the kept promotion serves the same build", result.RemovedRecordKeys)
	}
	if strings.Join(result.SurvivingRecordKeys, ",") != "record:web/b1" {
		t.Fatalf("surviving record keys = %v, want the build the kept promotion serves", result.SurvivingRecordKeys)
	}
	if _, held, err := l.Record(ctx, "web", "b1"); err != nil || !held {
		t.Fatalf("the record the kept promotion serves = held %v, %v, want it kept", held, err)
	}
}

func TestATagIsFreedWithThePromotionItNamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}, "", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p2"}, "", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Prune(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.claimTag(ctx, edge.Promotion{PromotionID: "p3", Tag: "live"}); err != nil {
		t.Fatalf("claim a tag whose promotion was pruned = %v, want it free", err)
	}
}

func TestSchemaIsWrittenAndReadBack(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, err := l.SchemaVersion(ctx); !errors.Is(err, edge.ErrStoreSchemaUnreadable) {
		t.Fatalf("SchemaVersion() before EnsureSchema = %v, want it unreadable", err)
	}
	if err := l.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := l.SchemaVersion(ctx)
	if err != nil || version != edge.StoreSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v, want %d", version, err, edge.StoreSchemaVersion)
	}
}

func TestPointersAndDestroy(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()

	for _, pointer := range []string{"", "staging"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: "p-" + pointer}, pointer, edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	pointers, err := l.Pointers(ctx)
	if err != nil || strings.Join(pointers, ",") != edge.DefaultPointer+",staging" {
		t.Fatalf("Pointers() = %v, %v", pointers, err)
	}
	if err := l.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if len(records.held) != 0 {
		t.Fatalf("Destroy() left %d records behind", len(records.held))
	}
}

func TestStagedRecordsRoundTrip(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if _, found, err := l.Record(ctx, "web", "abc"); err != nil || found {
		t.Fatalf("Record() before staging = %v, %v, want nothing found", found, err)
	}
	staged := edge.DeploymentRecord{App: "web", Identity: "abc"}
	if err := l.PutStaged(ctx, staged); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.Record(ctx, "web", "abc")
	if err != nil || !found || got.App != "web" || got.Identity != "abc" {
		t.Fatalf("Record() = %+v, %v, %v", got, found, err)
	}
	if err := l.PutStaged(ctx, edge.DeploymentRecord{App: "web"}); err == nil {
		t.Fatal("PutStaged() with no identity succeeded, want it refused")
	}
}

func activeIn(t *testing.T, l *Ledger, pointer string) string {
	t.Helper()
	held, err := l.pointerAt(context.Background(), pointerOr(pointer))
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func TestUnpromotingPutsThePointerBackOnThePromotionItDisplaced(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	for _, id := range []string{"p1", "p2"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, "", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}

	if err := l.Unpromote(ctx, "p2", ""); err != nil {
		t.Fatalf("Unpromote(p2) = %v", err)
	}
	if held := activeIn(t, l, ""); held != "p1" {
		t.Errorf("the pointer holds %q after p2 was taken back, want p1: an edge that could not serve p2 is still serving p1", held)
	}
	entries, err := l.History(ctx, "")
	if err != nil || len(entries) != 2 {
		t.Errorf("history = %v, %v, want p2 still recorded so `ocel rollback --to p2` can reach it", entries, err)
	}
}

func TestUnpromotingTheFirstPromotionLeavesThePointerAtNothing(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1"}, "staging", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}

	if err := l.Unpromote(ctx, "p1", "staging"); err != nil {
		t.Fatalf("Unpromote(p1) = %v", err)
	}
	if held := activeIn(t, l, "staging"); held != "" {
		t.Errorf("the pointer holds %q after its first promotion was taken back, want nothing: the edge serves nothing under it", held)
	}
}

func TestUnpromotingLeavesAPointerAnotherPromotionHasSinceTaken(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	for _, id := range []string{"p1", "p2", "p3"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, "", edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}

	if err := l.Unpromote(ctx, "p2", ""); err != nil {
		t.Fatalf("Unpromote(p2) = %v", err)
	}
	if held := activeIn(t, l, ""); held != "p3" {
		t.Errorf("the pointer holds %q, want p3: taking p2 back must not undo the promotion that followed it", held)
	}

	if err := l.Unpromote(ctx, "p3", ""); err != nil {
		t.Fatalf("Unpromote(p3) = %v", err)
	}
	if held := activeIn(t, l, ""); held != "p1" {
		t.Errorf("the pointer holds %q once p3 was taken back too, want p1: p2 was taken back first, so the edge never served it and p1 is what it still serves", held)
	}
}

func promoting(t *testing.T, l *Ledger, promotions ...edge.Promotion) {
	t.Helper()
	for _, promotion := range promotions {
		if err := l.Promote(context.Background(), promotion, "", edge.DiscardReporter()); err != nil {
			t.Fatalf("Promote(%s) = %v", promotion.PromotionID, err)
		}
	}
}

func historyOf(t *testing.T, l *Ledger) []string {
	t.Helper()
	entries, err := l.History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.PromotionID)
	}
	return ids
}

func TestARollbackTakenBackWhileALaterPromotionHeldThePointerLeavesTheReleaseItRolledOffServing(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, edge.Promotion{PromotionID: "p1"}, edge.Promotion{PromotionID: "p2"}, edge.Promotion{PromotionID: "p3"})
	promoting(t, l, edge.Promotion{PromotionID: "p2"}, edge.Promotion{PromotionID: "p4"})

	if err := l.Unpromote(ctx, "p2", ""); err != nil {
		t.Fatalf("Unpromote(p2) = %v", err)
	}
	if err := l.Unpromote(ctx, "p4", ""); err != nil {
		t.Fatalf("Unpromote(p4) = %v", err)
	}
	if held := activeIn(t, l, ""); held != "p3" {
		t.Errorf("the pointer holds %q, want p3: the rollback onto p2 and the promotion of p4 were both taken back, so the edge still serves p3", held)
	}
}

func TestARollbackTakenBackKeepsItsPlaceInHistory(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, edge.Promotion{PromotionID: "p1", Ts: 1}, edge.Promotion{PromotionID: "p2", Ts: 2}, edge.Promotion{PromotionID: "p3", Ts: 3})
	promoting(t, l, edge.Promotion{PromotionID: "p2", Ts: 9})

	if err := l.Unpromote(ctx, "p2", ""); err != nil {
		t.Fatalf("Unpromote(p2) = %v", err)
	}
	if got, want := historyOf(t, l), []string{"p3", "p2", "p1"}; !slices.Equal(got, want) {
		t.Errorf("history reads %v after a rollback onto p2 was taken back, want %v: the next default rollback from p3 is p2, not p1", got, want)
	}
	entries, err := l.History(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if entries[1].Ts != 2 {
		t.Errorf("p2 reads as created at %d, want 2: the rollback that was taken back never served it", entries[1].Ts)
	}
}

func TestATagTheEdgeNeverServedIsFreeForTheDeployThatRetriesIt(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, edge.Promotion{PromotionID: "p1", Tag: "v1"})

	if err := l.Unpromote(ctx, "p1", ""); err != nil {
		t.Fatalf("Unpromote(p1) = %v", err)
	}
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p2", Tag: "v1"}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("a retried deploy tagged v1 = %v, want it to claim the tag: p1 was taken back and never served", err)
	}
	entries, err := l.History(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Tag == "v1" && entry.PromotionID != "p2" {
			t.Errorf("history still tags %s as v1 beside p2, so `ocel rollback --tag v1` names two releases", entry.PromotionID)
		}
	}
}

func TestARollbackTakenBackKeepsTheTagItsReleaseAlreadyHeld(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()
	promoting(t, l, edge.Promotion{PromotionID: "p1", Tag: "v1"}, edge.Promotion{PromotionID: "p2"})
	promoting(t, l, edge.Promotion{PromotionID: "p1", Tag: "v1"})

	if err := l.Unpromote(ctx, "p1", ""); err != nil {
		t.Fatalf("Unpromote(p1) = %v", err)
	}
	var refusal ports.Refusal
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p3", Tag: "v1"}, "", edge.DiscardReporter()); !errors.As(err, &refusal) || refusal.Code != ports.CodeInvalid {
		t.Errorf("a new deploy tagged v1 = %v, want it refused: v1 still names p1, which served before the rollback onto it was taken back", err)
	}
}

func TestAPromotionThatLostThePointerRaceFreesItsTag(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()
	promoting(t, l, edge.Promotion{PromotionID: "p1"})
	pointer := l.pointerName(edge.DefaultPointer).String()
	records.racing = func(name string) {
		if name != pointer {
			return
		}
		records.racing = nil
		held := records.held[pointer]
		held.Revision = "another deploy got here"
		records.held[pointer] = held
	}
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p2", Tag: "v1"}, "", edge.DiscardReporter()); err == nil {
		t.Fatal("a promotion onto a moved pointer succeeded")
	}

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p3", Tag: "v1"}, "", edge.DiscardReporter()); err != nil {
		t.Errorf("re-running the deploy tagged v1 = %v, want the tag free: the refusal told the user to re-run it", err)
	}
}

func TestPromoteRefusesAPointerThatMovedAfterItWasRead(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1"}, "", edge.DiscardReporter()); err != nil {
		t.Fatal(err)
	}
	pointer := l.pointerName(edge.DefaultPointer).String()
	promotion := l.promotionName(edge.DefaultPointer, "p2").String()
	records.racing = func(name string) {
		if name != promotion {
			return
		}
		records.racing = nil
		racer := ports.Record{Name: l.pointerName(edge.DefaultPointer), Bytes: []byte(`{"promotionId":"p9"}`), Revision: records.held[pointer].Revision}
		if _, err := records.Write(ctx, racer); err != nil {
			t.Fatal(err)
		}
	}

	err := l.Promote(ctx, edge.Promotion{PromotionID: "p2"}, "", edge.DiscardReporter())
	var refusal ports.Refusal
	if !errors.As(err, &refusal) || refusal.Code != ports.CodeBusy {
		t.Fatalf("promote onto a pointer another deploy moved after it was read = %v, want a busy refusal: the promotion records what it displaced, and a pointer that moved displaced something else", err)
	}
	if held := activeIn(t, l, ""); held != "p9" {
		t.Errorf("the pointer holds %q, want the winner's p9", held)
	}
}
