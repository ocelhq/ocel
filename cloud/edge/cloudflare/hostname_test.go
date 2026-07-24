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

	existingRoutes        []map[string]any
	existingRecords       []map[string]any
	existingCustomDomains []map[string]any

	createdRoutes        []map[string]any
	createdRecords       []map[string]any
	deletedRecords       []string
	deletedRoutes        []string
	deletedCustomDomains []string
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

	mux.HandleFunc("/zones/"+m.zoneID+"/workers/routes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			id := strings.TrimPrefix(r.URL.Path, "/zones/"+m.zoneID+"/workers/routes/")
			m.deletedRoutes = append(m.deletedRoutes, id)
			writeResult(w, map[string]any{"id": id})
		}
	})

	mux.HandleFunc("/accounts/acct/workers/domains", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, m.existingCustomDomains)
	})

	mux.HandleFunc("/accounts/acct/workers/domains/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			id := strings.TrimPrefix(r.URL.Path, "/accounts/acct/workers/domains/")
			m.deletedCustomDomains = append(m.deletedCustomDomains, id)
			writeResult(w, map[string]any{"id": id})
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
// hostname — without it the hostname never resolves and the route never fires.
func TestReconcileWorkerRoutes_PlantsProxiedRecord(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"*.preview.app.com"}, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
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

// Production may attach several hostnames — an apex and a "www" alias, say —
// each becoming its own worker route with its own placeholder record.
func TestReconcileWorkerRoutes_AttachesEveryDomain(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com", "www.app.com"}, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	patterns := map[string]bool{}
	for _, r := range m.createdRoutes {
		patterns[r["pattern"].(string)] = true
	}
	if !patterns["app.com/*"] || !patterns["www.app.com/*"] {
		t.Errorf("created route patterns = %v, want both app.com/* and www.app.com/*", patterns)
	}
	if len(m.createdRecords) != 2 {
		t.Errorf("expected a placeholder record per hostname, got %d", len(m.createdRecords))
	}
}

// The route path is idempotent: a redeploy that finds the placeholder record
// already present must not create a second one.
func TestReconcileWorkerRoutes_ExistingRecordIsLeftAlone(t *testing.T) {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"*.preview.app.com"}, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRoutes) != 0 {
		t.Errorf("expected no route created, got %d", len(m.createdRoutes))
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("expected no DNS record created, got %d", len(m.createdRecords))
	}
}

// Dropping a hostname from the config tears down its route and Ocel's own
// placeholder record; routes for other scripts, and routes still wanted, stay.
func TestReconcileWorkerRoutes_PrunesDroppedDomain(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "stale", "pattern": "www.app.com/*", "script": "ocel-prod"},
			{"id": "kept", "pattern": "app.com/*", "script": "ocel-prod"},
			{"id": "other", "pattern": "x.app.com/*", "script": "someone-else"},
		},
		existingRecords: []map[string]any{
			{"id": "wwwrec", "name": "www.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com"}, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "stale" {
		t.Errorf("deleted routes = %v, want [stale]", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 1 || m.deletedRecords[0] != "wwwrec" {
		t.Errorf("deleted records = %v, want [wwwrec]", m.deletedRecords)
	}
}

// The pivot off custom domains is self-healing: a redeploy detaches any custom
// domain still bound to the script (a route overlapping it would be rejected).
func TestReconcileWorkerRoutes_DetachesLeftoverCustomDomains(t *testing.T) {
	m := &cfMock{
		zoneID:                "zone1",
		zoneName:              "app.com",
		existingCustomDomains: []map[string]any{{"id": "cd1", "hostname": "app.com", "service": "ocel-prod"}},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com"}, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedCustomDomains) != 1 || m.deletedCustomDomains[0] != "cd1" {
		t.Errorf("detached custom domains = %v, want [cd1]", m.deletedCustomDomains)
	}
}

// A hostname the zone's Universal SSL does not cover (more than one label deep)
// warns rather than silently serving a broken TLS handshake.
func TestReconcileWorkerRoutes_WarnsOnUncoveredTLS(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	var warnings []string
	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"*.preview.app.com"}, func(s string) {
		warnings = append(warnings, s)
	}); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], "Advanced Certificate") {
		t.Errorf("warnings = %v, want one about an Advanced Certificate", warnings)
	}
}

// An existing unproxied record at a route hostname means the route cannot fire;
// the deploy warns and leaves the user's record untouched.
func TestReconcileWorkerRoutes_WarnsOnUnproxiedRecord(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "user", "name": "app.com", "type": "A", "content": "203.0.113.1", "proxied": false},
		},
	}
	p := m.provider(t)

	var warnings []string
	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com"}, func(s string) {
		warnings = append(warnings, s)
	}); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRecords) != 0 {
		t.Errorf("must not overwrite the user's record, created %d", len(m.createdRecords))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "proxied") {
		t.Errorf("warnings = %v, want one about proxying the record", warnings)
	}
}

// A production apex almost always carries TXT/MX records (SPF, verification)
// that share its name but can never be proxied. Those must not be mistaken for
// an address record: the route path still plants its AAAA placeholder and warns
// about nothing.
func TestReconcileWorkerRoutes_PlantsDespiteTXTRecord(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "spf", "name": "app.com", "type": "TXT", "content": "v=spf1 -all", "proxied": false},
		},
	}
	p := m.provider(t)

	var warnings []string
	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com"}, func(s string) {
		warnings = append(warnings, s)
	}); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRecords) != 1 {
		t.Errorf("expected the AAAA placeholder to be planted, created %d", len(m.createdRecords))
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// A proxied address record already at the hostname means the route resolves:
// leave it, plant nothing, warn about nothing.
func TestReconcileWorkerRoutes_ProxiedAddressRecordLeftAlone(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "user", "name": "app.com", "type": "A", "content": "203.0.113.1", "proxied": true},
		},
	}
	p := m.provider(t)

	var warnings []string
	up := upload{accountID: "acct", scriptName: "ocel-prod"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, []string{"app.com"}, func(s string) {
		warnings = append(warnings, s)
	}); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRecords) != 0 {
		t.Errorf("expected no record created, got %d", len(m.createdRecords))
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
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
