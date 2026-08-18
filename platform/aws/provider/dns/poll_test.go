package dns

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func testPoller(lookup Lookup) (Poller, *int) {
	waits := 0
	return Poller{
		Lookup:   lookup,
		Wait:     func(context.Context, time.Duration) error { waits++; return nil },
		Attempts: 3,
	}, &waits
}

func TestPollerAwait(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("says what to add and returns once it resolves", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		var probed string
		poller, waits := testPoller(func(_ context.Context, host string) ([]string, error) {
			attempts++
			probed = host
			if attempts < 2 {
				return nil, errors.New("no such host")
			}
			return []string{"203.0.113.1"}, nil
		})

		var said []string
		if err := poller.Await(t.Context(), []edge.Record{wildcard}, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("Await: %v", err)
		}
		if len(said) == 0 || !strings.Contains(said[0], "*.preview.app.com") {
			t.Errorf("said = %v, want the record to add named first", said)
		}
		if !strings.Contains(said[0], "proxied") {
			t.Errorf("said = %q, want the proxied record spelled out", said[0])
		}
		if strings.Contains(probed, "*") {
			t.Errorf("probed %q, want a wildcard probed at a real label", probed)
		}
		if *waits != 1 {
			t.Errorf("waits = %d, want one wait between the two attempts", *waits)
		}
	})

	t.Run("gives up naming the record after the bound", func(t *testing.T) {
		t.Parallel()

		poller, waits := testPoller(func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") })

		err := poller.Await(t.Context(), []edge.Record{wildcard}, func(string) {})
		if err == nil {
			t.Fatal("Await err = nil, want the wait to be given up on")
		}
		if !strings.Contains(err.Error(), "*.preview.app.com") {
			t.Errorf("err = %q, want it to name the record", err)
		}
		if !strings.Contains(err.Error(), "route53()") || !strings.Contains(err.Error(), "cloudflareDns()") {
			t.Errorf("err = %q, want it to name the descriptors that would write it", err)
		}
		if *waits != 2 {
			t.Errorf("waits = %d, want one fewer wait than attempts", *waits)
		}
	})

	t.Run("nothing to await resolves at once", func(t *testing.T) {
		t.Parallel()

		poller, _ := testPoller(func(context.Context, string) ([]string, error) {
			t.Error("looked a record up with nothing to await")
			return nil, nil
		})
		if err := poller.Await(t.Context(), nil, func(string) {}); err != nil {
			t.Fatalf("Await(nil): %v", err)
		}
	})
}

type deletingWriter struct {
	deleted []edge.Record
}

func (d *deletingWriter) EnsureRecords(context.Context, []edge.Record, func(string)) ([]edge.Record, error) {
	return nil, nil
}

func (d *deletingWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	d.deleted = append(d.deleted, records...)
	return nil
}

func TestRelease(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("a writer deletes what was recorded", func(t *testing.T) {
		t.Parallel()

		writer := &deletingWriter{}
		if err := Release(t.Context(), writer, []edge.Record{wildcard}, func(string) {}); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if len(writer.deleted) != 1 || writer.deleted[0] != wildcard {
			t.Errorf("deleted = %v, want %v", writer.deleted, wildcard)
		}
	})

	t.Run("no writer names every record left standing", func(t *testing.T) {
		t.Parallel()

		var said []string
		if err := Release(t.Context(), nil, []edge.Record{wildcard}, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if len(said) != 1 || !strings.Contains(said[0], "*.preview.app.com") {
			t.Errorf("said = %v, want the abandoned record named", said)
		}
	})

	t.Run("nothing recorded says nothing", func(t *testing.T) {
		t.Parallel()

		if err := Release(t.Context(), nil, nil, func(string) {
			t.Error("said something with no records to release")
		}); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})
}
