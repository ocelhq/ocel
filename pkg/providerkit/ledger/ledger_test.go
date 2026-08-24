package ledger

import (
	"context"
	"errors"
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

	if err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatal(err)
	}
	err := l.claimTag(ctx, edge.Promotion{PromotionID: "p2", Tag: "live"})
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

	if err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := l.claimTag(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}); err != nil {
		t.Fatalf("the holder reclaiming its own tag = %v, want it allowed", err)
	}
}

func TestPromoteRefusesAPointerAnotherDeployMoved(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1"}, ""); err != nil {
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

	err := l.Promote(ctx, edge.Promotion{PromotionID: "p2"}, "")
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
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, ""); err != nil {
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
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id, Builds: map[string]string{"web": id}}, ""); err != nil {
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

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "old"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p2", "p3", "p4"} {
		if err := l.Promote(ctx, edge.Promotion{PromotionID: id}, "staging"); err != nil {
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

func TestATagIsFreedWithThePromotionItNamed(t *testing.T) {
	l, _ := fixture()
	ctx := context.Background()

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1", Tag: "live"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p2"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Prune(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := l.claimTag(ctx, edge.Promotion{PromotionID: "p3", Tag: "live"}); err != nil {
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
		if err := l.Promote(ctx, edge.Promotion{PromotionID: "p-" + pointer}, pointer); err != nil {
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
