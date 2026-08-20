package domains

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeWriter struct {
	written []edge.Record
	deleted []edge.Record
	err     error
}

func (f *fakeWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.written = append(f.written, records...)
	return records, nil
}

func (f *fakeWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	f.deleted = append(f.deleted, records...)
	return nil
}

func TestSettleRecords(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("a writer writes the record and it lands on the state", func(t *testing.T) {
		t.Parallel()

		writer := &fakeWriter{}
		engine := Engine{
			Writer: writer,
			Poller: dns.Poller{Lookup: func(context.Context, string) ([]string, error) {
				t.Error("polled DNS with a writer in hand")
				return nil, nil
			}},
		}

		var recorded Records
		settled, err := engine.settleRecords(t.Context(), []edge.Record{wildcard}, func([]edge.Record) {
			t.Error("asked the user for a record with a writer in hand")
		}, true, func(string) {}, func(checkpointed Records) error {
			recorded = checkpointed
			return nil
		})
		if err != nil {
			t.Fatalf("settleRecords: %v", err)
		}
		if len(writer.written) != 1 || writer.written[0] != wildcard {
			t.Errorf("written = %v, want %v", writer.written, wildcard)
		}
		if len(recorded.Written) != 1 || recorded.Written[0] != wildcard {
			t.Errorf("recorded = %v, want the written record on the state", recorded.Written)
		}
		if len(settled.Owed) != 0 {
			t.Errorf("owed = %v, want nothing owed: ocel wrote the record", settled.Owed)
		}
	})

	t.Run("no writer asks the user for the record, records nothing and polls", func(t *testing.T) {
		t.Parallel()

		lookups := 0
		engine := Engine{
			Poller: dns.Poller{
				Lookup: func(context.Context, string) ([]string, error) {
					lookups++
					return []string{"203.0.113.1"}, nil
				},
				Attempts: 2,
			},
		}

		var said []string
		var asked []edge.Record
		recorded := Records{Written: []edge.Record{wildcard}}
		_, err := engine.settleRecords(t.Context(), []edge.Record{wildcard}, func(records []edge.Record) {
			asked = records
		}, true, func(m string) { said = append(said, m) }, func(checkpointed Records) error {
			recorded = checkpointed
			return nil
		})
		if err != nil {
			t.Fatalf("settleRecords: %v", err)
		}
		if lookups == 0 {
			t.Error("lookups = 0, want the record polled for")
		}
		if len(recorded.Written) != 0 {
			t.Errorf("recorded = %v, want nothing recorded: ocel wrote nothing", recorded.Written)
		}
		if len(recorded.Owed) != 1 || recorded.Owed[0] != wildcard {
			t.Errorf("owed = %v, want the record the user still owes", recorded.Owed)
		}
		if len(asked) != 1 || asked[0] != wildcard {
			t.Errorf("asked = %v, want the record handed to the user to add", asked)
		}
		if len(said) == 0 || !strings.Contains(said[0], "*.preview.acme.com") {
			t.Errorf("said = %v, want the record waited on named", said)
		}
	})

	t.Run("validation records are never polled for: ACM does the waiting", func(t *testing.T) {
		t.Parallel()

		engine := Engine{
			Poller: dns.Poller{Lookup: func(context.Context, string) ([]string, error) {
				t.Error("polled DNS for a validation record")
				return nil, nil
			}},
		}
		if _, err := engine.settleRecords(t.Context(), []edge.Record{wildcard}, nil, false, func(string) {}, func(Records) error {
			return nil
		}); err != nil {
			t.Fatalf("settleRecords: %v", err)
		}
	})

	t.Run("a failed write is surfaced, but the domain is still recorded", func(t *testing.T) {
		t.Parallel()

		writer := &fakeWriter{err: errors.New("zone is read-only")}
		engine := Engine{Writer: writer, Poller: dns.Poller{}}
		recorded := Records{Written: []edge.Record{wildcard}}
		wrote := false
		_, err := engine.settleRecords(t.Context(), []edge.Record{wildcard}, nil, true, func(string) {}, func(checkpointed Records) error {
			wrote, recorded = true, checkpointed
			return nil
		})
		if err == nil {
			t.Fatal("settleRecords err = nil, want the write failure surfaced")
		}
		if !wrote {
			t.Error("the domain was not recorded, so nothing is left to release the edge entry with")
		}
		if len(recorded.Written) != 0 {
			t.Errorf("recorded = %v, want no records: the write failed", recorded.Written)
		}
	})
}
