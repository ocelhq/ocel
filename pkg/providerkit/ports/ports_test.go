package ports_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type moving struct {
	ports.RecordStore
	held    ports.Record
	moves   int
	removed bool
}

func (m *moving) Read(context.Context, ports.RecordName) (ports.Record, error) {
	if m.removed {
		return ports.Record{}, ports.ErrNoRecord
	}
	return m.held, nil
}

func (m *moving) Remove(context.Context, ports.RecordName, ports.Revision) error {
	if m.moves > 0 {
		m.moves--
		m.held.Revision += "'"
		return ports.ErrStale
	}
	m.removed = true
	return nil
}

func TestForgetReadsAgainWhenTheRecordMovedUnderIt(t *testing.T) {
	records := &moving{held: ports.Record{Name: ports.RecordName{"values", "shop", "production"}, Revision: "one"}, moves: 2}
	if err := ports.Forget(context.Background(), records, records.held.Name); err != nil {
		t.Fatalf("Forget() of a record rewritten twice = %v, want it removed at the revision it settled on", err)
	}
	if !records.removed {
		t.Fatal("Forget() reported the record gone while it still stood")
	}
}

func TestForgetRefusesToReportARecordGoneThatKeepsMoving(t *testing.T) {
	records := &moving{held: ports.Record{Name: ports.RecordName{"values", "shop", "production"}, Revision: "one"}, moves: 100}
	if err := ports.Forget(context.Background(), records, records.held.Name); err == nil {
		t.Fatal("Forget() of a record it never removed = nil, and every caller reads that as removed")
	}
}
