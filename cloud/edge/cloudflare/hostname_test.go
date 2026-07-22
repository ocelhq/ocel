package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
)

// cfMock is a minimal stand-in for the Cloudflare REST API covering the calls
// the worker-route path makes: list the account's zones, list/create worker
// routes in a zone, and list/create/delete DNS records in a zone. It records
// the create/delete requests so tests can assert what the provider did.
type cfMock struct {
	zoneID, zoneName string

	existingRoutes  []map[string]any
	existingRecords []map[string]any

	createdRoutes  []map[string]any
	createdRecords []map[string]any
	deletedRecords []string
}

func (m *cfMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// V4 paginated lists terminate when a page comes back empty; page 2+ is empty.
	firstPage := func(r *http.Request) bool {
		p := r.URL.Query().Get("page")
		return p == "" || p == "1"
	}
	writeResult := func(w http.ResponseWriter, result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "errors": []any{}, "messages": []any{},
			"result": result,
			"result_info": map[string]any{
				"page": 1, "per_page": 100, "count": 1, "total_count": 1,
			},
		})
	}

	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if !firstPage(r) {
			writeResult(w, []any{})
			return
		}
		writeResult(w, []map[string]any{{"id": m.zoneID, "name": m.zoneName}})
	})

	mux.HandleFunc("/zones/"+m.zoneID+"/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeResult(w, m.existingRoutes)
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.createdRoutes = append(m.createdRoutes, body)
			writeResult(w, map[string]any{"id": "route-new", "pattern": body["pattern"], "script": body["script"]})
		}
	})

	mux.HandleFunc("/zones/"+m.zoneID+"/dns_records", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if !firstPage(r) {
				writeResult(w, []any{})
				return
			}
			writeResult(w, m.existingRecords)
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.createdRecords = append(m.createdRecords, body)
			writeResult(w, map[string]any{"id": "record-new"})
		}
	})

	mux.HandleFunc("/zones/"+m.zoneID+"/dns_records/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			id := strings.TrimPrefix(r.URL.Path, "/zones/"+m.zoneID+"/dns_records/")
			m.deletedRecords = append(m.deletedRecords, id)
			writeResult(w, map[string]any{"id": id})
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (m *cfMock) provider(t *testing.T) *provider {
	srv := m.server(t)
	return &provider{client: cf.NewClient(
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIToken("test"),
	)}
}

// A worker route only matches traffic that already reaches Cloudflare's edge, so
// the route path must also plant a proxied placeholder DNS record for the
// wildcard hostname — without it the hostname never resolves and the route never
// fires.
func TestReconcileWorkerRoute_PlantsProxiedRecord(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	if err := p.reconcileWorkerRoute(context.Background(), up, "*.preview.app.com"); err != nil {
		t.Fatalf("reconcileWorkerRoute: %v", err)
	}

	if len(m.createdRoutes) != 1 {
		t.Fatalf("expected one route created, got %d", len(m.createdRoutes))
	}
	if got := m.createdRoutes[0]["pattern"]; got != "*.preview.app.com/*" {
		t.Errorf("route pattern = %v, want *.preview.app.com/*", got)
	}

	if len(m.createdRecords) != 1 {
		t.Fatalf("expected one DNS record created, got %d", len(m.createdRecords))
	}
	rec := m.createdRecords[0]
	if rec["name"] != "*.preview.app.com" {
		t.Errorf("record name = %v, want *.preview.app.com", rec["name"])
	}
	if rec["type"] != "AAAA" {
		t.Errorf("record type = %v, want AAAA", rec["type"])
	}
	if rec["content"] != "100::" {
		t.Errorf("record content = %v, want 100::", rec["content"])
	}
	if rec["proxied"] != true {
		t.Errorf("record proxied = %v, want true", rec["proxied"])
	}
}

// The route path is idempotent: a redeploy that finds the placeholder record
// already present must not create a second one.
func TestReconcileWorkerRoute_ExistingRecordIsLeftAlone(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
		},
		existingRecords: []map[string]any{
			{"id": "record1", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	if err := p.reconcileWorkerRoute(context.Background(), up, "*.preview.app.com"); err != nil {
		t.Fatalf("reconcileWorkerRoute: %v", err)
	}

	if len(m.createdRoutes) != 0 {
		t.Errorf("expected no route created, got %d", len(m.createdRoutes))
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("expected no DNS record created, got %d", len(m.createdRecords))
	}
}

// Destroying a worker removes the placeholder records the route path planted for
// it — script deletion drops the routes, but not the DNS records that made their
// hostnames resolve — while leaving records the user manages untouched.
func TestDetachRouteRecords_RemovesOnlyOcelPlaceholders(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
			{"id": "route2", "pattern": "other.app.com/*", "script": "someone-else"},
		},
		existingRecords: []map[string]any{
			{"id": "ours", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	if err := p.detachRouteRecords(context.Background(), "acct", "ocel-preview"); err != nil {
		t.Fatalf("detachRouteRecords: %v", err)
	}

	if len(m.deletedRecords) != 1 || m.deletedRecords[0] != "ours" {
		t.Errorf("deleted records = %v, want [ours]", m.deletedRecords)
	}
}

// A DNS record the user owns at the route's hostname (not our discard-prefix
// placeholder) is never deleted on teardown.
func TestDetachRouteRecords_LeavesUserRecords(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
		},
		existingRecords: []map[string]any{
			{"id": "user", "name": "*.preview.app.com", "type": "AAAA", "content": "2606:4700::1", "proxied": true},
		},
	}
	p := m.provider(t)

	if err := p.detachRouteRecords(context.Background(), "acct", "ocel-preview"); err != nil {
		t.Fatalf("detachRouteRecords: %v", err)
	}

	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none", m.deletedRecords)
	}
}
