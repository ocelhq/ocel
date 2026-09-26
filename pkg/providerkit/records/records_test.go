package records_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
)

type moving struct {
	records.Store
	recorded records.Record
	moves    int
	removed  bool
}

func (m *moving) Read(context.Context, records.Name) (records.Record, error) {
	if m.removed {
		return records.Record{}, records.ErrNotFound
	}
	return m.recorded, nil
}

func (m *moving) Remove(context.Context, records.Name, records.Revision) error {
	if m.moves > 0 {
		m.moves--
		m.recorded.Revision += "'"
		return records.ErrStale
	}
	m.removed = true
	return nil
}

func TestForgetReadsAgainWhenTheRecordMovedUnderIt(t *testing.T) {
	moved := &moving{recorded: records.Record{Name: records.Name{"values", "shop", "production"}, Revision: "one"}, moves: 2}
	if err := records.Forget(context.Background(), moved, moved.recorded.Name); err != nil {
		t.Fatalf("Forget() of a record rewritten twice = %v, want it removed at the revision it ended at", err)
	}
	if !moved.removed {
		t.Fatal("Forget() reported the record gone while it still existed")
	}
}

func TestForgetRefusesToReportARecordGoneThatKeepsMoving(t *testing.T) {
	moved := &moving{recorded: records.Record{Name: records.Name{"values", "shop", "production"}, Revision: "one"}, moves: 100}
	if err := records.Forget(context.Background(), moved, moved.recorded.Name); err == nil {
		t.Fatal("Forget() of a record it never removed = nil, and every caller reads that as removed")
	}
}
