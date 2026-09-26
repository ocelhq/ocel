package records_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
)

type moving struct {
	records.Store
	held    records.Record
	moves   int
	removed bool
}

func (m *moving) Read(context.Context, records.Name) (records.Record, error) {
	if m.removed {
		return records.Record{}, records.ErrNotFound
	}
	return m.held, nil
}

func (m *moving) Remove(context.Context, records.Name, records.Revision) error {
	if m.moves > 0 {
		m.moves--
		m.held.Revision += "'"
		return records.ErrStale
	}
	m.removed = true
	return nil
}

func TestForgetReadsAgainWhenTheRecordMovedUnderIt(t *testing.T) {
	moved := &moving{held: records.Record{Name: records.Name{"values", "shop", "production"}, Revision: "one"}, moves: 2}
	if err := records.Forget(context.Background(), moved, moved.held.Name); err != nil {
		t.Fatalf("Forget() of a record rewritten twice = %v, want it removed at the revision it settled on", err)
	}
	if !moved.removed {
		t.Fatal("Forget() reported the record gone while it still stood")
	}
}

func TestForgetRefusesToReportARecordGoneThatKeepsMoving(t *testing.T) {
	moved := &moving{held: records.Record{Name: records.Name{"values", "shop", "production"}, Revision: "one"}, moves: 100}
	if err := records.Forget(context.Background(), moved, moved.held.Name); err == nil {
		t.Fatal("Forget() of a record it never removed = nil, and every caller reads that as removed")
	}
}
