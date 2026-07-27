package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	putScripts           []string
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

	mux.HandleFunc("PUT /accounts/acct/workers/scripts/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.putScripts = append(m.putScripts, r.PathValue("name"))
		writeResult(w, map[string]any{"id": r.PathValue("name")})
	})

	mux.HandleFunc("POST /accounts/acct/workers/scripts/{name}/subdomain", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, map[string]any{"enabled": false, "previews_enabled": false})
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
			writeResult(w, matchingRecords(m.existingRecords, r.URL.Query()))
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

// matchingRecords applies the "name.exact" and "type" filters the way the real
// API does. Serving the whole zone regardless would let an assertion that only
// one hostname's record was touched pass vacuously.
func matchingRecords(records []map[string]any, query url.Values) []map[string]any {
	matched := []map[string]any{}
	for _, rec := range records {
		if name := query.Get("name.exact"); name != "" && rec["name"] != name {
			continue
		}
		if recordType := query.Get("type"); recordType != "" && rec["type"] != recordType {
			continue
		}
		matched = append(matched, rec)
	}
	return matched
}

func (m *cfMock) provider(t *testing.T) *provider {
	srv := m.server(t)
	return &provider{client: cf.NewClient(
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIToken("test"),
	)}
}

// prunedPlan is the production-shaped route plan: these hostnames, each resolved
// by Ocel's own placeholder record, and any other route on the script pruned.
func prunedPlan(desired ...string) routePlan {
	return routePlan{desired: desired, prune: true}
}

// A worker route only matches traffic that already reaches Cloudflare's edge, so
// the route path must also plant a proxied placeholder DNS record for the
// hostname — without it the hostname never resolves and the route never fires.
func TestReconcileWorkerRoutes_PlantsProxiedRecord(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("*.preview.app.com"), nil); err != nil {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com", "www.app.com"), nil); err != nil {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("*.preview.app.com"), nil); err != nil {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com"), nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "stale" {
		t.Errorf("deleted routes = %v, want [stale]", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 1 || m.deletedRecords[0] != "wwwrec" {
		t.Errorf("deleted records = %v, want [wwwrec]", m.deletedRecords)
	}
}

// ocelhq-5w3: a preview app's pointers share one worker script and hold one
// exact route each, and a reconcile knows only the pointer it is deploying — so
// pruning would black-hole the pointers deploying concurrently beside it.
func TestReconcileWorkerRoutes_WithoutPruningLeavesSiblingRoutes(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "sibling", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
		},
		existingRecords: []map[string]any{
			{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	plan := routePlan{desired: []string{"pr-2-abc1234567.preview.app.com"}, requiredRecord: "*.preview.app.com"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "pr-2-abc1234567.preview.app.com/*" {
		t.Errorf("created routes = %v, want just this pointer's own route", m.createdRoutes)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none: a sibling pointer's route is not this reconcile's to prune", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none", m.deletedRecords)
	}
}

// A required record is shared by every hostname under the base domain, so a
// reconcile confirms it and plants nothing of its own.
func TestReconcileWorkerRoutes_RequiredRecordPresentPlantsNothing(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	plan := routePlan{desired: []string{"pr-1-abc1234567.preview.app.com"}, requiredRecord: "*.preview.app.com"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.createdRoutes) != 1 {
		t.Errorf("created routes = %v, want the pointer's route", m.createdRoutes)
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("created records = %v, want none: the required record is not this deploy's to plant", m.createdRecords)
	}
}

// Without the required record the pointer hostname never resolves, so the deploy
// fails up front naming the record to add rather than shipping a dead hostname.
func TestReconcileWorkerRoutes_RequiredRecordMissingFails(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	plan := routePlan{desired: []string{"pr-1-abc1234567.preview.app.com"}, requiredRecord: "*.preview.app.com"}
	err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil)
	if err == nil {
		t.Fatal("reconcileWorkerRoutes err = nil, want a failure for the missing required record")
	}
	if !strings.Contains(err.Error(), "*.preview.app.com") {
		t.Errorf("error = %v, want it to name the record to add", err)
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("created records = %v, want none: reconcile must never create the required record", m.createdRecords)
	}
}

// An unproxied record never reaches a worker, so it is as fatal as a missing one.
func TestReconcileWorkerRoutes_RequiredRecordUnproxiedFails(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": false},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-preview"}
	plan := routePlan{desired: []string{"pr-1-abc1234567.preview.app.com"}, requiredRecord: "*.preview.app.com"}
	err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil)
	if err == nil {
		t.Fatal("reconcileWorkerRoutes err = nil, want a failure for the unproxied required record")
	}
	if !strings.Contains(err.Error(), "proxied") || !strings.Contains(err.Error(), "*.preview.app.com") {
		t.Errorf("error = %v, want it to name the record and say it must be proxied", err)
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com"), nil); err != nil {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("*.preview.app.com"), func(s string) {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com"), func(s string) {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com"), func(s string) {
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
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("app.com"), func(s string) {
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

// ocelhq-5w3: every project under a preview base domain resolves through the
// wildcard record there, byte-identical though it is to a record Ocel plants.
// What keeps a teardown off it is that nothing routes the wildcard itself: a
// pointer holds an exact route, and teardown only reclaims the record at a
// hostname its own routes name.
func TestDetachRouteRecords_LeavesTheSharedPreviewBaseRecord(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
			{"id": "route2", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "ocel-preview"},
		},
		existingRecords: []map[string]any{
			{"id": "base", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	if err := p.detachRouteRecords(context.Background(), "acct", "ocel-preview"); err != nil {
		t.Fatalf("detachRouteRecords: %v", err)
	}

	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none: every other deployment under *.preview.app.com resolves through it", m.deletedRecords)
	}
}
