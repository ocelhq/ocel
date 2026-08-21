package ledger

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type store struct {
	held   map[string]providerkit.Record
	rev    int
	racing func(name string)
}

func newStore() *store { return &store{held: map[string]providerkit.Record{}} }

func (s *store) Read(_ context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	held, ok := s.held[name.String()]
	if !ok {
		return providerkit.Record{Name: name}, nil
	}
	return held, nil
}

func (s *store) Write(_ context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if s.racing != nil {
		s.racing(record.Name.String())
	}
	if held := s.held[record.Name.String()]; held.Revision != record.Revision {
		return "", providerkit.ErrStale
	}
	s.rev++
	record.Revision = providerkit.Revision(strconv.Itoa(s.rev))
	s.held[record.Name.String()] = record
	return record.Revision, nil
}

func (s *store) Remove(_ context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	if held, ok := s.held[name.String()]; ok && held.Revision != expected {
		return providerkit.ErrStale
	}
	delete(s.held, name.String())
	return nil
}

func (s *store) List(_ context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	var out []providerkit.Record
	for key, record := range s.held {
		if strings.HasPrefix(key, under.String()+"/") {
			out = append(out, record)
		}
	}
	return out, nil
}

func fixture() (*Ledger, *store) {
	records := newStore()
	return New(records, providerkit.ClassProduction, "shop"), records
}

func TestNextSequenceRetriesPastAClaimerThatGotThereFirst(t *testing.T) {
	l, records := fixture()
	ctx := context.Background()
	first, err := l.nextSequence(ctx)
	if err != nil || first != 1 {
		t.Fatalf("first sequence = %d, %v", first, err)
	}
	stale, _ := records.Read(ctx, l.sequenceName())
	if _, err := l.nextSequence(ctx); err != nil {
		t.Fatal(err)
	}
	stale.Bytes = []byte("99")
	if _, err := records.Write(ctx, stale); !errors.Is(err, providerkit.ErrStale) {
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
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeOccupied {
		t.Fatalf("second claim = %v, want an occupied refusal", err)
	}
	if !strings.Contains(refusal.Message, "p1") {
		t.Fatalf("the refusal does not name the holder: %s", refusal.Message)
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
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("promote onto a moved pointer = %v, want a busy refusal", err)
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
	entries, err := l.History(ctx, "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("history after prune = %d entries, %v", len(entries), err)
	}
}
