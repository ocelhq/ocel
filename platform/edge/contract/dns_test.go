package edge

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSelectZone(t *testing.T) {
	t.Parallel()

	zones := []Zone{
		{ID: "z-app", Name: "app.com"},
		{ID: "z-preview", Name: "preview.app.com"},
		{ID: "z-other", Name: "elsewhere.com"},
	}

	cases := []struct {
		name     string
		hostname string
		named    string
		want     string
		wantErr  string
	}{
		{name: "the named zone wins over a longer match", hostname: "a.preview.app.com", named: "app.com", want: "z-app"},
		{name: "the longest suffix wins", hostname: "a.preview.app.com", want: "z-preview"},
		{name: "the zone itself is owned", hostname: "app.com", want: "z-app"},
		{name: "a wildcard is owned by the zone under it", hostname: "*.preview.app.com", want: "z-preview"},
		{name: "a hostname declared in mixed case is owned all the same", hostname: "Shop.App.com", want: "z-app"},
		{name: "a zone named in mixed case is the zone", hostname: "shop.app.com", named: "App.COM", want: "z-app"},
		{name: "no zone owns the hostname", hostname: "shop.nowhere.com", wantErr: "no zone reachable"},
		{name: "a named zone that is not there", hostname: "app.com", named: "missing.com", wantErr: "no zone named"},
		{name: "a named zone that owns nothing of the sort", hostname: "app.com", named: "elsewhere.com", wantErr: "does not own"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := SelectZone(zones, tc.hostname, tc.named)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("SelectZone(%q, %q) = %v, want an error", tc.hostname, tc.named, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("SelectZone(%q, %q) err = %v, want it to contain %q", tc.hostname, tc.named, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectZone(%q, %q) error = %v", tc.hostname, tc.named, err)
			}
			if got.ID != tc.want {
				t.Errorf("SelectZone(%q, %q) = %v, want %v", tc.hostname, tc.named, got.ID, tc.want)
			}
		})
	}
}

func TestRecordsFor(t *testing.T) {
	t.Parallel()

	t.Run("a cloudflare edge takes the proxied placeholder", func(t *testing.T) {
		t.Parallel()

		got, err := RecordsFor(DNSTarget{Kind: KindCloudflare}, []string{"*.preview.app.com"})
		if err != nil {
			t.Fatalf("RecordsFor error = %v", err)
		}
		want := Record{Name: "*.preview.app.com", Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true}
		if len(got) != 1 || got[0] != want {
			t.Errorf("RecordsFor = %v, want %v", got, want)
		}
	})

	for _, kind := range []Kind{KindNative, KindNone} {
		t.Run("a "+string(kind)+" edge takes a grey-cloud CNAME to the front", func(t *testing.T) {
			t.Parallel()

			got, err := RecordsFor(DNSTarget{Kind: kind, Front: "d123.cloudfront.net"}, []string{"shop.app.com"})
			if err != nil {
				t.Fatalf("RecordsFor error = %v", err)
			}
			want := Record{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "d123.cloudfront.net"}
			if len(got) != 1 || got[0] != want {
				t.Errorf("RecordsFor = %v, want %v", got, want)
			}
			if got[0].Proxied {
				t.Errorf("record is proxied, want grey cloud")
			}
		})
	}

	t.Run("a front-less non-cloudflare edge names what is missing", func(t *testing.T) {
		t.Parallel()

		if _, err := RecordsFor(DNSTarget{Kind: KindNative}, []string{"shop.app.com"}); err == nil {
			t.Fatal("RecordsFor err = nil, want a refusal: nothing to point the hostname at")
		}
	})
}

func TestWrittenRecords(t *testing.T) {
	t.Parallel()

	records := []Record{
		{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "front"},
		{Name: "a.app.com", Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true},
		{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "front"},
	}

	state, err := WithWrittenRecords(StackState{StackKeySlug: "shop"}, records)
	if err != nil {
		t.Fatalf("WithWrittenRecords error = %v", err)
	}
	if state[StackKeySlug] != "shop" {
		t.Errorf("slug = %q, want the rest of the state carried over", state[StackKeySlug])
	}

	got, err := WrittenRecords(state)
	if err != nil {
		t.Fatalf("WrittenRecords error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("WrittenRecords = %v, want the two distinct records", got)
	}
	if got[0].Name != "a.app.com" || got[1].Name != "shop.app.com" {
		t.Errorf("WrittenRecords = %v, want them sorted by name", got)
	}

	empty, err := WithWrittenRecords(state, nil)
	if err != nil {
		t.Fatalf("WithWrittenRecords(nil) error = %v", err)
	}
	if _, ok := empty[StackKeyRecords]; ok {
		t.Errorf("state = %v, want the key gone once nothing is written", empty)
	}
}

func TestRecordApexNote(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		record Record
		zone   string
		apex   bool
	}{
		{name: "the zone itself", record: Record{Name: "app.com", Type: RecordTypeCNAME}, zone: "app.com", apex: true},
		{name: "a multi-label zone", record: Record{Name: "example.co.uk", Type: RecordTypeCNAME}, zone: "example.co.uk", apex: true},
		{name: "a host under the zone", record: Record{Name: "shop.app.com", Type: RecordTypeCNAME}, zone: "app.com"},
		{name: "a two-label host under a two-label zone", record: Record{Name: "foo.internal", Type: RecordTypeCNAME}, zone: "bar.internal"},
		{name: "an address record at the zone", record: Record{Name: "app.com", Type: RecordTypeAAAA}, zone: "app.com"},
		{name: "no zone in hand falls back to the label count", record: Record{Name: "app.com", Type: RecordTypeCNAME}, apex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			note := tc.record.ApexNote(tc.zone)
			if tc.apex && !strings.Contains(note, "ALIAS") {
				t.Errorf("ApexNote(%q) = %q, want the alias-or-flattening note", tc.zone, note)
			}
			if !tc.apex && note != "" {
				t.Errorf("ApexNote(%q) = %q, want nothing said", tc.zone, note)
			}
		})
	}
}

type plainWriter struct{}

func (plainWriter) EnsureRecords(context.Context, []Record, func(string)) ([]Record, error) {
	return nil, nil
}

func (plainWriter) DeleteRecords(context.Context, []Record) error { return nil }

type ttlWriter struct {
	plainWriter
	ttl time.Duration
}

func (w ttlWriter) RecordTTL() time.Duration { return w.ttl }

func TestWriteTTL(t *testing.T) {
	t.Parallel()

	if got := WriteTTL(ttlWriter{ttl: 90 * time.Second}); got != 90*time.Second {
		t.Errorf("WriteTTL = %s, want the TTL the writer serves its records with", got)
	}
	if got := WriteTTL(plainWriter{}); got != 0 {
		t.Errorf("WriteTTL = %s, want nothing claimed for a writer that names no TTL", got)
	}
	if got := WriteTTL(nil); got != 0 {
		t.Errorf("WriteTTL = %s, want nothing claimed when no writer writes", got)
	}
}
