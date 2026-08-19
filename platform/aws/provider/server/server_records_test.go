package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeDNSWriter struct {
	written []edge.Record
	deleted []edge.Record
	err     error
}

func (f *fakeDNSWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.written = append(f.written, records...)
	return records, nil
}

func (f *fakeDNSWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	f.deleted = append(f.deleted, records...)
	return nil
}

func TestSettleRecords(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("a writer writes the record and it lands on the state", func(t *testing.T) {
		t.Parallel()

		writer := &fakeDNSWriter{}
		var recorded []edge.Record
		poller := dns.Poller{Lookup: func(context.Context, string) ([]string, error) {
			t.Error("polled DNS with a writer in hand")
			return nil, nil
		}}

		var owedBack []edge.Record
		err := settleRecords(t.Context(), writer, poller, []edge.Record{wildcard}, func(string) {}, func([]edge.Record) {
			t.Error("asked the user for a record with a writer in hand")
		}, func(written, owed []edge.Record) error {
			recorded, owedBack = written, owed
			return nil
		})
		if err != nil {
			t.Fatalf("settleRecords: %v", err)
		}
		if len(writer.written) != 1 || writer.written[0] != wildcard {
			t.Errorf("written = %v, want %v", writer.written, wildcard)
		}
		if len(recorded) != 1 || recorded[0] != wildcard {
			t.Errorf("recorded = %v, want the written record on the state", recorded)
		}
		if len(owedBack) != 0 {
			t.Errorf("owed = %v, want nothing owed: ocel wrote the record", owedBack)
		}
	})

	t.Run("no writer asks the user for the record, records nothing and polls", func(t *testing.T) {
		t.Parallel()

		lookups := 0
		poller := dns.Poller{
			Lookup: func(context.Context, string) ([]string, error) {
				lookups++
				return []string{"203.0.113.1"}, nil
			},
			Attempts: 2,
		}

		var said []string
		var asked []edge.Record
		var owed []edge.Record
		recorded := []edge.Record{wildcard}
		err := settleRecords(t.Context(), nil, poller, []edge.Record{wildcard}, func(m string) { said = append(said, m) }, func(records []edge.Record) {
			asked = records
		}, func(written, missing []edge.Record) error {
			recorded, owed = written, missing
			return nil
		})
		if err != nil {
			t.Fatalf("settleRecords: %v", err)
		}
		if lookups == 0 {
			t.Error("lookups = 0, want the record polled for")
		}
		if len(recorded) != 0 {
			t.Errorf("recorded = %v, want nothing recorded: ocel wrote nothing", recorded)
		}
		if len(owed) != 1 || owed[0] != wildcard {
			t.Errorf("owed = %v, want the record the user still owes", owed)
		}
		if len(asked) != 1 || asked[0] != wildcard {
			t.Errorf("asked = %v, want the record handed to the user to add", asked)
		}
		if len(said) == 0 || !strings.Contains(said[0], "*.preview.acme.com") {
			t.Errorf("said = %v, want the record waited on named", said)
		}
	})

	t.Run("a failed write is surfaced, but the domain is still recorded", func(t *testing.T) {
		t.Parallel()

		writer := &fakeDNSWriter{err: errors.New("zone is read-only")}
		recorded := []edge.Record{wildcard}
		wrote := false
		err := settleRecords(t.Context(), writer, dns.Poller{}, []edge.Record{wildcard}, func(string) {}, nil, func(written, _ []edge.Record) error {
			wrote, recorded = true, written
			return nil
		})
		if err == nil {
			t.Fatal("settleRecords err = nil, want the write failure surfaced")
		}
		if !wrote {
			t.Error("the domain was not recorded, so nothing is left to release the edge entry with")
		}
		if len(recorded) != 0 {
			t.Errorf("recorded = %v, want no records: the write failed", recorded)
		}
	})
}
