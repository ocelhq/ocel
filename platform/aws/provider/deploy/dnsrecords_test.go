package deploy

import (
	"context"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordingDNS struct {
	written []edge.Record
	deleted []edge.Record
}

func (r *recordingDNS) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	r.written = append(r.written, records...)
	return records, nil
}

func (r *recordingDNS) DeleteRecords(_ context.Context, records []edge.Record) error {
	r.deleted = append(r.deleted, records...)
	return nil
}

type recordingWaiter struct {
	awaited []edge.Record
}

func (r *recordingWaiter) Await(_ context.Context, records []edge.Record, _ func(string)) error {
	r.awaited = append(r.awaited, records...)
	return nil
}

func TestSettleStackRecords(t *testing.T) {
	t.Parallel()

	specs := []edge.StackSpec{{Domains: []string{"shop.app.com"}}, {Domains: nil}}
	wildcard := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("a writer writes every declared host and records it on the stack", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{}, DNS: writer}

		state, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{edge.StackKeySlug: "shop"}, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 1 || writer.written[0] != wildcard {
			t.Errorf("written = %v, want %v", writer.written, wildcard)
		}
		got, err := edge.WrittenRecords(state)
		if err != nil {
			t.Fatalf("WrittenRecords: %v", err)
		}
		if len(got) != 1 || got[0] != wildcard {
			t.Errorf("recorded records = %v, want %v", got, wildcard)
		}
	})

	t.Run("without a writer the record is waited on and nothing is recorded", func(t *testing.T) {
		t.Parallel()

		waiter := &recordingWaiter{}
		cfg := Config{Edge: &recordingEdge{}, DNSAwait: waiter}

		state, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{}, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(waiter.awaited) != 1 || waiter.awaited[0] != wildcard {
			t.Errorf("awaited = %v, want %v", waiter.awaited, wildcard)
		}
		if _, ok := state[edge.StackKeyRecords]; ok {
			t.Errorf("state = %v, want no records recorded: ocel wrote none", state)
		}
	})

	t.Run("a stack serving no hostname of its own writes nothing", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{}, DNS: writer}

		if _, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{}}, edge.StackState{}, func(string) {}); err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.written) != 0 {
			t.Errorf("written = %v, want none", writer.written)
		}
	})

	t.Run("a non-cloudflare edge with no front to name is refused", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Edge: &recordingEdge{kind: edge.KindNative}, DNS: &recordingDNS{}}

		if _, err := settleStackRecords(t.Context(), cfg, specs, edge.StackState{}, func(string) {}); err == nil {
			t.Fatal("settleStackRecords err = nil, want a refusal: nothing to point the hostname at")
		}
	})

	t.Run("a hostname dropped from the deploy has its record deleted", func(t *testing.T) {
		t.Parallel()

		gone := edge.Record{Name: "old.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		prior, err := edge.WithWrittenRecords(edge.StackState{}, []edge.Record{wildcard, gone})
		if err != nil {
			t.Fatalf("WithWrittenRecords: %v", err)
		}
		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{}, DNS: writer, StackState: prior}

		state, err := settleStackRecords(t.Context(), cfg, specs, prior, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != gone {
			t.Errorf("deleted = %v, want %v", writer.deleted, gone)
		}
		kept, err := edge.WrittenRecords(state)
		if err != nil {
			t.Fatalf("WrittenRecords: %v", err)
		}
		if len(kept) != 1 || kept[0] != wildcard {
			t.Errorf("recorded records = %v, want only %v", kept, wildcard)
		}
	})

	t.Run("a stack serving nothing any more gives up every record it wrote", func(t *testing.T) {
		t.Parallel()

		prior, err := edge.WithWrittenRecords(edge.StackState{}, []edge.Record{wildcard})
		if err != nil {
			t.Fatalf("WithWrittenRecords: %v", err)
		}
		writer := &recordingDNS{}
		cfg := Config{Edge: &recordingEdge{}, DNS: writer, StackState: prior}

		state, err := settleStackRecords(t.Context(), cfg, []edge.StackSpec{{}}, prior, func(string) {})
		if err != nil {
			t.Fatalf("settleStackRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != wildcard {
			t.Errorf("deleted = %v, want %v", writer.deleted, wildcard)
		}
		if _, ok := state[edge.StackKeyRecords]; ok {
			t.Errorf("state = %v, want no records recorded", state)
		}
	})
}

func TestReleaseRecords(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	state, err := edge.WithWrittenRecords(edge.StackState{}, []edge.Record{wildcard})
	if err != nil {
		t.Fatalf("WithWrittenRecords: %v", err)
	}

	t.Run("teardown deletes every record the stack wrote", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		if err := releaseRecords(t.Context(), Config{DNS: writer}, state, func(string) {}); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != wildcard {
			t.Errorf("deleted = %v, want %v", writer.deleted, wildcard)
		}
	})

	t.Run("teardown without a writer says what it leaves behind", func(t *testing.T) {
		t.Parallel()

		var said []string
		if err := releaseRecords(t.Context(), Config{}, state, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(said) != 1 || !strings.Contains(said[0], "shop.app.com") {
			t.Errorf("said = %v, want the abandoned record named", said)
		}
	})

	t.Run("a stack that wrote nothing says nothing", func(t *testing.T) {
		t.Parallel()

		writer := &recordingDNS{}
		if err := releaseRecords(t.Context(), Config{DNS: writer}, edge.StackState{}, func(string) {
			t.Error("said something about a stack that wrote no records")
		}); err != nil {
			t.Fatalf("releaseRecords: %v", err)
		}
		if len(writer.deleted) != 0 {
			t.Errorf("deleted = %v, want none", writer.deleted)
		}
	})
}
