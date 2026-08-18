package cloudflare

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeRecords struct {
	existing map[string][]dns.RecordResponse
	created  []dns.RecordNewParams
	updated  []string
	deleted  []string
}

func (f *fakeRecords) List(_ context.Context, params dns.RecordListParams, _ ...option.RequestOption) (*pagination.V4PagePaginationArray[dns.RecordResponse], error) {
	if params.Page.Value > 1 {
		return &pagination.V4PagePaginationArray[dns.RecordResponse]{}, nil
	}
	return &pagination.V4PagePaginationArray[dns.RecordResponse]{
		Result: f.existing[params.ZoneID.Value+"|"+params.Name.Value.Exact.Value],
	}, nil
}

func (f *fakeRecords) New(_ context.Context, params dns.RecordNewParams, _ ...option.RequestOption) (*dns.RecordResponse, error) {
	f.created = append(f.created, params)
	return &dns.RecordResponse{}, nil
}

func (f *fakeRecords) Update(_ context.Context, recordID string, _ dns.RecordUpdateParams, _ ...option.RequestOption) (*dns.RecordResponse, error) {
	f.updated = append(f.updated, recordID)
	return &dns.RecordResponse{}, nil
}

func (f *fakeRecords) Delete(_ context.Context, recordID string, _ dns.RecordDeleteParams, _ ...option.RequestOption) (*dns.RecordDeleteResponse, error) {
	f.deleted = append(f.deleted, recordID)
	return &dns.RecordDeleteResponse{}, nil
}

type fakeZones struct {
	owned []zones.Zone
	lists int
}

func (f *fakeZones) List(_ context.Context, params zones.ZoneListParams, _ ...option.RequestOption) (*pagination.V4PagePaginationArray[zones.Zone], error) {
	if params.Page.Value > 1 {
		return &pagination.V4PagePaginationArray[zones.Zone]{}, nil
	}
	f.lists++
	return &pagination.V4PagePaginationArray[zones.Zone]{Result: f.owned}, nil
}

func newTestWriter(records *fakeRecords, owned []zones.Zone, named string) (*dnsWriter, *fakeZones) {
	if records.existing == nil {
		records.existing = map[string][]dns.RecordResponse{}
	}
	zoneList := &fakeZones{owned: owned}
	return &dnsWriter{records: records, zones: zoneList, accountID: testAccountID, named: named}, zoneList
}

var testZones = []zones.Zone{
	{ID: "zone-app", Name: "app.com"},
	{ID: "zone-preview", Name: "preview.app.com"},
}

func createdBody(t *testing.T, params dns.RecordNewParams) map[string]any {
	t.Helper()
	raw, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal created record: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode created record: %v", err)
	}
	return body
}

func TestDNSWriterEnsureRecords(t *testing.T) {
	t.Parallel()

	t.Run("writes a proxied placeholder into the longest matching zone", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{}
		w, zoneList := newTestWriter(records, testZones, "")
		want := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		if _, err := w.EnsureRecords(t.Context(), []edge.Record{want}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(records.created) != 1 {
			t.Fatalf("created = %v, want one record", records.created)
		}
		if got := records.created[0].ZoneID.Value; got != "zone-preview" {
			t.Errorf("zone = %q, want %q", got, "zone-preview")
		}
		body := createdBody(t, records.created[0])
		if body["type"] != "AAAA" || body["content"] != edge.ProxyPlaceholder || body["proxied"] != true {
			t.Errorf("created record = %v, want a proxied AAAA placeholder", body)
		}
		if zoneList.lists != 1 {
			t.Errorf("zone lists = %d, want the zones read once", zoneList.lists)
		}
	})

	t.Run("writes a grey-cloud CNAME to the front", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{}
		w, _ := newTestWriter(records, testZones, "")
		want := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}
		if _, err := w.EnsureRecords(t.Context(), []edge.Record{want}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		body := createdBody(t, records.created[0])
		if body["type"] != "CNAME" || body["content"] != "front.example.net" || body["proxied"] != false {
			t.Errorf("created record = %v, want a grey-cloud CNAME", body)
		}
	})

	t.Run("writes into the named zone", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{}
		w, _ := newTestWriter(records, testZones, "app.com")
		want := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		if _, err := w.EnsureRecords(t.Context(), []edge.Record{want}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if got := records.created[0].ZoneID.Value; got != "zone-app" {
			t.Errorf("zone = %q, want the named zone %q", got, "zone-app")
		}
	})

	t.Run("no zone owning the hostname is named in the refusal", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{}
		w, _ := newTestWriter(records, testZones, "")
		_, err := w.EnsureRecords(t.Context(), []edge.Record{{Name: "shop.elsewhere.com", Type: edge.RecordTypeCNAME, Value: "front"}}, nil)
		if err == nil {
			t.Fatal("EnsureRecords err = nil, want a refusal for a hostname outside the account's zones")
		}
		if !strings.Contains(err.Error(), "shop.elsewhere.com") {
			t.Errorf("err = %q, want it to name the hostname", err)
		}
		if len(records.created) != 0 {
			t.Errorf("created = %v, want nothing written", records.created)
		}
	})

	t.Run("a record already carrying the value is left alone", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{existing: map[string][]dns.RecordResponse{
			"zone-app|shop.app.com": {{ID: "live", Type: dns.RecordResponseTypeCNAME, Content: "front.example.net"}},
		}}
		w, _ := newTestWriter(records, testZones, "")
		if _, err := w.EnsureRecords(t.Context(), []edge.Record{{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(records.created) != 0 || len(records.updated) != 0 {
			t.Errorf("created = %v, updated = %v, want the live record untouched", records.created, records.updated)
		}
	})

	t.Run("a record ocel wrote is repointed, a user's own is not", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{existing: map[string][]dns.RecordResponse{
			"zone-app|ours.app.com":   {{ID: "ours", Type: dns.RecordResponseTypeCNAME, Content: "old.example.net", Comment: recordComment}},
			"zone-app|theirs.app.com": {{ID: "theirs", Type: dns.RecordResponseTypeCNAME, Content: "their.example.net"}},
		}}
		w, _ := newTestWriter(records, testZones, "")
		written, err := w.EnsureRecords(t.Context(), []edge.Record{
			{Name: "ours.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"},
			{Name: "theirs.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"},
		}, nil)
		if err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(records.updated) != 1 || records.updated[0] != "ours" {
			t.Errorf("updated = %v, want only the record ocel wrote", records.updated)
		}
		if len(records.created) != 0 {
			t.Errorf("created = %v, want a user's own record left standing", records.created)
		}
		if len(written) != 1 || written[0].Name != "ours.app.com" {
			t.Errorf("written = %v, want only the record ocel owns", written)
		}
	})

	t.Run("a foreign record is left standing, said out loud and never recorded", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{existing: map[string][]dns.RecordResponse{
			"zone-app|shop.app.com": {{ID: "theirs", Type: dns.RecordResponseTypeA, Content: "203.0.113.9"}},
		}}
		w, _ := newTestWriter(records, testZones, "")
		var said []string
		want := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		written, err := w.EnsureRecords(t.Context(), []edge.Record{want}, func(m string) { said = append(said, m) })
		if err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(records.created) != 0 || len(records.updated) != 0 {
			t.Errorf("created = %v, updated = %v, want a record ocel does not own untouched", records.created, records.updated)
		}
		if len(written) != 0 {
			t.Errorf("written = %v, want nothing recorded: ocel wrote nothing", written)
		}
		if len(said) != 1 || !strings.Contains(said[0], "shop.app.com") {
			t.Errorf("said = %v, want the dark hostname named", said)
		}
	})

	t.Run("a foreign record listed first does not hide ocel's own", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{existing: map[string][]dns.RecordResponse{
			"zone-app|shop.app.com": {
				{ID: "theirs", Type: dns.RecordResponseTypeA, Content: "203.0.113.9"},
				{ID: "ours", Type: dns.RecordResponseTypeAAAA, Content: "100::", Comment: recordComment},
			},
		}}
		w, _ := newTestWriter(records, testZones, "")
		want := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}
		written, err := w.EnsureRecords(t.Context(), []edge.Record{want}, nil)
		if err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(records.updated) != 1 || records.updated[0] != "ours" {
			t.Errorf("updated = %v, want ocel's own record repointed", records.updated)
		}
		if len(written) != 1 || written[0] != want {
			t.Errorf("written = %v, want %v", written, want)
		}
	})

	t.Run("a foreign record already serving the hostname says nothing", func(t *testing.T) {
		t.Parallel()

		records := &fakeRecords{existing: map[string][]dns.RecordResponse{
			"zone-app|shop.app.com": {{ID: "theirs", Type: dns.RecordResponseTypeAAAA, Content: "100::", Proxied: true}},
		}}
		w, _ := newTestWriter(records, testZones, "")
		var said []string
		want := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		if _, err := w.EnsureRecords(t.Context(), []edge.Record{want}, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(said) != 0 {
			t.Errorf("said = %v, want silence: the hostname already serves", said)
		}
	})
}

func TestDNSWriterDeleteRecords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		live dns.RecordResponse
		want []string
	}{
		{
			name: "deletes a record whose value still matches",
			live: dns.RecordResponse{ID: "ours", Type: dns.RecordResponseTypeAAAA, Content: edge.ProxyPlaceholder},
			want: []string{"ours"},
		},
		{
			name: "leaves a record whose value has moved on",
			live: dns.RecordResponse{ID: "moved", Type: dns.RecordResponseTypeAAAA, Content: "2001:db8::1"},
		},
		{
			name: "leaves a record of another type at the same name",
			live: dns.RecordResponse{ID: "other", Type: dns.RecordResponseTypeCNAME, Content: edge.ProxyPlaceholder},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			records := &fakeRecords{existing: map[string][]dns.RecordResponse{
				"zone-preview|*.preview.app.com": {tc.live},
			}}
			w, _ := newTestWriter(records, testZones, "")
			written := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
			if err := w.DeleteRecords(t.Context(), []edge.Record{written}); err != nil {
				t.Fatalf("DeleteRecords: %v", err)
			}
			if len(records.deleted) != len(tc.want) {
				t.Fatalf("deleted = %v, want %v", records.deleted, tc.want)
			}
			for i, id := range tc.want {
				if records.deleted[i] != id {
					t.Errorf("deleted = %v, want %v", records.deleted, tc.want)
				}
			}
		})
	}
}
