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

type cfMock struct {
	zoneID, zoneName string

	existingRoutes        []map[string]any
	existingRecords       []map[string]any
	existingCustomDomains []map[string]any
	existingScripts       []string

	createdRoutes        []map[string]any
	repointedRoutes      []string
	createdRecords       []map[string]any
	deletedRecords       []string
	deletedRoutes        []string
	deletedCustomDomains []string
	putScripts           []string
}

func (m *cfMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

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
		id := strings.TrimPrefix(r.URL.Path, "/zones/"+m.zoneID+"/workers/routes/")
		switch r.Method {
		case http.MethodDelete:
			m.deletedRoutes = append(m.deletedRoutes, id)
			writeResult(w, map[string]any{"id": id})
		case http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.repointedRoutes = append(m.repointedRoutes, id)
			writeResult(w, map[string]any{"id": id, "pattern": body["pattern"], "script": body["script"]})
		}
	})

	mux.HandleFunc("GET /accounts/acct/workers/scripts", func(w http.ResponseWriter, r *http.Request) {
		if !firstPage(r) {
			writeResult(w, []any{})
			return
		}
		scripts := []map[string]any{}
		for _, name := range m.existingScripts {
			scripts = append(scripts, map[string]any{"id": name})
		}
		writeResult(w, scripts)
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

func prunedPlan(desired ...string) routePlan {
	return routePlan{desired: desired, prune: true}
}

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

func TestRoutePattern_IsTheOneSpellingOfAHostnameAsARoute(t *testing.T) {
	if got := RoutePattern("*.preview.app.com"); got != "*.preview.app.com/*" {
		t.Errorf("RoutePattern = %q, want *.preview.app.com/*", got)
	}
}

func TestRouteOwner_ReportsTheScriptBoundToThePattern(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-other-preview"},
		},
	}
	p := m.provider(t)

	owner, err := p.RouteOwner(context.Background(), RoutePattern("*.preview.app.com"))
	if err != nil {
		t.Fatalf("RouteOwner: %v", err)
	}
	if owner != "ocel-other-preview" {
		t.Errorf("owner = %q, want ocel-other-preview", owner)
	}
	if len(m.createdRoutes)+len(m.createdRecords)+len(m.deletedRoutes) != 0 {
		t.Error("RouteOwner changed the zone; it is read-only")
	}
}

func TestRouteOwner_UnheldPatternIsUnclaimed(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}

	owner, err := m.provider(t).RouteOwner(context.Background(), RoutePattern("*.preview.app.com"))
	if err != nil {
		t.Fatalf("RouteOwner: %v", err)
	}
	if owner != "" {
		t.Errorf("owner = %q, want empty for a pattern nothing holds", owner)
	}
}

func TestRouteOwner_OverlappingWildcardIsNotAMatch(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "route1", "pattern": "*.app.com/*", "script": "ocel-other-preview"},
		},
	}

	owner, err := m.provider(t).RouteOwner(context.Background(), RoutePattern("*.preview.app.com"))
	if err != nil {
		t.Fatalf("RouteOwner: %v", err)
	}
	if owner != "" {
		t.Errorf("owner = %q, want empty: *.app.com/* is not the pattern asked for", owner)
	}
}

func TestRouteOwner_HostnameOutsideTheAccountIsAnError(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}

	if _, err := m.provider(t).RouteOwner(context.Background(), RoutePattern("*.preview.elsewhere.com")); err == nil {
		t.Fatal("RouteOwner err = nil, want the unresolvable zone reported")
	}
}

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

func TestReconcileWorkerRoutes_PrunesRoutesOnWorkersUnderTheStem(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "perapp", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shop-preview-web"},
		},
		existingRecords: []map[string]any{
			{"id": "perapprec", "name": "pr-1.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-shop-preview"}
	plan := routePlan{desired: []string{"*.preview.app.com"}, prune: true, pruneStem: "ocel-shop-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "perapp" {
		t.Errorf("deleted routes = %v, want [perapp]", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 1 || m.deletedRecords[0] != "perapprec" {
		t.Errorf("deleted records = %v, want [perapprec]", m.deletedRecords)
	}
}

func TestReconcileWorkerRoutes_StemNeverReachesAnotherWorkerFamily(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "sibling", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shopfoo-preview-web"},
			{"id": "other", "pattern": "pr-2.preview.app.com/*", "script": "ocel-other-preview"},
			{"id": "prod", "pattern": "app.com/*", "script": "ocel-shop-prod-web"},
			{"id": "lookalike", "pattern": "pr-3.preview.app.com/*", "script": "ocel-shop-previewer"},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-shop-preview"}
	plan := routePlan{desired: []string{"*.preview.app.com"}, prune: true, pruneStem: "ocel-shop-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none: no route here is under the stem", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none", m.deletedRecords)
	}
}

func TestReconcileWorkerRoutes_StemNeverTakesADesiredHostname(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "wildcard", "pattern": "*.preview.app.com/*", "script": "ocel-shop-preview-web"},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-shop-preview"}
	plan := routePlan{desired: []string{"*.preview.app.com"}, prune: true, pruneStem: "ocel-shop-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, plan, nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.repointedRoutes) != 1 || m.repointedRoutes[0] != "wildcard" {
		t.Errorf("repointed routes = %v, want [wildcard]", m.repointedRoutes)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none: the wildcard was repointed, not pruned", m.deletedRoutes)
	}
}

func TestReconcileWorkerRoutes_NoStemSweepsThisScriptOnly(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "perapp", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shop-preview-web"},
		},
	}
	p := m.provider(t)

	up := upload{accountID: "acct", scriptName: "ocel-shop-preview"}
	if err := p.reconcileWorkerRoutes(context.Background(), up, prunedPlan("*.preview.app.com"), nil); err != nil {
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none: no stem was given", m.deletedRoutes)
	}
}

func TestReconcileWorkerRoutes_WithoutPruningLeavesUnnamedRoutes(t *testing.T) {
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRoutes: []map[string]any{
			{"id": "unnamed", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
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
		t.Errorf("created routes = %v, want just the desired route", m.createdRoutes)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none: a route this reconcile did not name is not its to prune", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none", m.deletedRecords)
	}
}

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
		t.Errorf("created routes = %v, want the desired route", m.createdRoutes)
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("created records = %v, want none: the required record is not this deploy's to plant", m.createdRecords)
	}
}

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

func TestDetachRouteRecords_LeavesARecordNoRouteOfItsOwnNames(t *testing.T) {
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
		t.Errorf("deleted records = %v, want none: no route on this script names *.preview.app.com", m.deletedRecords)
	}
}
