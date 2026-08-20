package edge

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	sampleKind       Kind = "sample"
	unboundKind      Kind = "serves-unbound"
	frontedKind      Kind = "fronted"
	otherFrontedKind Kind = "other-fronted"
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

	t.Run("an edge that serves unbound takes the proxied placeholder", func(t *testing.T) {
		t.Parallel()

		got, err := RecordsFor(DNSTarget{Kind: unboundKind, ServesUnbound: true}, []string{"*.preview.app.com"})
		if err != nil {
			t.Fatalf("RecordsFor error = %v", err)
		}
		want := Record{Name: "*.preview.app.com", Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true}
		if len(got) != 1 || got[0] != want {
			t.Errorf("RecordsFor = %v, want %v", got, want)
		}
	})

	for _, kind := range []Kind{frontedKind, otherFrontedKind} {
		t.Run("a "+string(kind)+" edge takes a grey-cloud CNAME to the front", func(t *testing.T) {
			t.Parallel()

			got, err := RecordsFor(DNSTarget{Kind: kind, Front: "front.example.net"}, []string{"shop.app.com"})
			if err != nil {
				t.Fatalf("RecordsFor error = %v", err)
			}
			want := Record{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "front.example.net"}
			if len(got) != 1 || got[0] != want {
				t.Errorf("RecordsFor = %v, want %v", got, want)
			}
			if got[0].Proxied {
				t.Errorf("record is proxied, want grey cloud")
			}
		})
	}

	t.Run("a front-less edge that binds names what is missing", func(t *testing.T) {
		t.Parallel()

		if _, err := RecordsFor(DNSTarget{Kind: frontedKind}, []string{"shop.app.com"}); err == nil {
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

	state := StackState{Slug: "shop"}
	state.RecordWrites(records)
	if state.Slug != "shop" {
		t.Errorf("slug = %q, want the rest of the state carried over", state.Slug)
	}
	if len(state.Records) != 2 {
		t.Fatalf("written records = %v, want the two distinct records", state.Records)
	}
	if state.Records[0].Name != "a.app.com" || state.Records[1].Name != "shop.app.com" {
		t.Errorf("written records = %v, want them sorted by name", state.Records)
	}
	if !slices.Equal(records, []Record{
		{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "front"},
		{Name: "a.app.com", Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true},
		{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "front"},
	}) {
		t.Errorf("records handed in = %v, want them left in the order they came", records)
	}

	state.RecordWrites(nil)
	if state.Records != nil {
		t.Errorf("written records = %v, want none once nothing is written", state.Records)
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

func TestRecordsForPerHostFronts(t *testing.T) {
	t.Parallel()

	t.Run("each host takes its own front where the stack published one", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.PublishFront("shop.app.com", "d-shop.execute-api.eu-west-1.amazonaws.com")
		state.PublishFront("www.app.com", "d-www.execute-api.eu-west-1.amazonaws.com")
		got, err := RecordsFor(TargetOf(otherFrontedKind, false, state), []string{"shop.app.com", "www.app.com"})
		if err != nil {
			t.Fatalf("RecordsFor error = %v", err)
		}
		want := []Record{
			{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "d-shop.execute-api.eu-west-1.amazonaws.com"},
			{Name: "www.app.com", Type: RecordTypeCNAME, Value: "d-www.execute-api.eu-west-1.amazonaws.com"},
		}
		if !slices.Equal(got, want) {
			t.Errorf("RecordsFor = %v, want %v", got, want)
		}
	})

	t.Run("one front stands for every host that has none of its own", func(t *testing.T) {
		t.Parallel()

		state := StackState{Front: "d123.cloudfront.net"}
		state.PublishFront("shop.app.com", "d-shop.execute-api.eu-west-1.amazonaws.com")
		got, err := RecordsFor(TargetOf(frontedKind, false, state), []string{"shop.app.com", "www.app.com"})
		if err != nil {
			t.Fatalf("RecordsFor error = %v", err)
		}
		want := []Record{
			{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "d-shop.execute-api.eu-west-1.amazonaws.com"},
			{Name: "www.app.com", Type: RecordTypeCNAME, Value: "d123.cloudfront.net"},
		}
		if !slices.Equal(got, want) {
			t.Errorf("RecordsFor = %v, want %v", got, want)
		}
	})

	t.Run("a host with no front of its own and no shared front is named", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.PublishFront("shop.app.com", "d-shop.execute-api.eu-west-1.amazonaws.com")
		_, err := RecordsFor(TargetOf(otherFrontedKind, false, state), []string{"shop.app.com", "www.app.com"})
		if err == nil {
			t.Fatal("RecordsFor err = nil, want a refusal: nothing to point www.app.com at")
		}
		if !strings.Contains(err.Error(), "www.app.com") {
			t.Errorf("err = %q, want it to name the host with no front", err)
		}
	})

	t.Run("an edge that serves unbound needs no front at all", func(t *testing.T) {
		t.Parallel()

		got, err := RecordsFor(TargetOf(unboundKind, true, StackState{}), []string{"shop.app.com"})
		if err != nil {
			t.Fatalf("RecordsFor error = %v", err)
		}
		want := Record{Name: "shop.app.com", Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true}
		if len(got) != 1 || got[0] != want {
			t.Errorf("RecordsFor = %v, want %v", got, want)
		}
	})

	t.Run("forgetting a host front leaves the others standing", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.PublishFront("shop.app.com", "d-shop")
		state.PublishFront("www.app.com", "d-www")
		left := state
		left.PublishFront("shop.app.com", "")
		if len(left.Fronts) != 1 || left.Fronts["www.app.com"] != "d-www" {
			t.Errorf("host fronts = %v, want only www.app.com", left.Fronts)
		}
		if len(state.Fronts) != 2 {
			t.Errorf("host fronts on the state copied off = %v, want both: forgetting one must not reach back", state.Fronts)
		}
	})
}

func TestPointable(t *testing.T) {
	t.Parallel()

	front := StackState{Front: "d111111abcdef8.cloudfront.net"}

	t.Run("an edge that binds a host before serving it points only what it bound", func(t *testing.T) {
		t.Parallel()

		for _, kind := range []Kind{frontedKind, otherFrontedKind} {
			target := TargetOf(kind, false, front)
			if Pointable(target, nil, "shop.app.com") {
				t.Errorf("Pointable(%s, unbound) = true, want the host left for the command that binds it", kind)
			}
			if !Pointable(target, []string{"shop.app.com"}, "shop.app.com") {
				t.Errorf("Pointable(%s, bound) = false, want the bound host pointed", kind)
			}
		}
	})

	t.Run("an edge that serves unbound points a host it has not bound, because the record is what binds it", func(t *testing.T) {
		t.Parallel()

		if !Pointable(TargetOf(unboundKind, true, StackState{}), nil, "shop.app.com") {
			t.Error("Pointable(serves-unbound, unbound) = false, want the proxied record that puts the host in service")
		}
	})
}

func TestZoneOwns(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		hostname, zone string
		want           bool
	}{
		{"a subdomain of the zone", "app.acme.com", "acme.com", true},
		{"the zone apex itself", "acme.com", "acme.com", true},
		{"a zone delegated at the subdomain", "app.acme.com", "app.acme.com", true},
		{"a zone recorded in another case", "app.ACME.com", "acme.COM", true},
		{"an unrelated zone", "app.acme.com", "other.com", false},
		{"a zone that is only a suffix of the label", "app.acme.com", "me.com", false},
		{"a hostname that merely ends in the zone name", "notacme.com", "acme.com", false},
		{"a zone sharing the tail of a label", "app.acme.com", "cme.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ZoneOwns(tc.hostname, tc.zone); got != tc.want {
				t.Errorf("ZoneOwns(%q, %q) = %v, want %v", tc.hostname, tc.zone, got, tc.want)
			}
		})
	}
}
