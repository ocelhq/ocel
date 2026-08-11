package cloudflare

import (
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

	zoneLists  int
	routeLists int

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
		m.zoneLists++
		writeResult(w, []map[string]any{{"id": m.zoneID, "name": m.zoneName}})
	})

	mux.HandleFunc("/zones/"+m.zoneID+"/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if firstPage(r) {
				m.routeLists++
			}
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

func stemPlan(stem string, desired ...string) routePlan {
	return routePlan{desired: desired, prune: true, pruneStem: stem}
}

func requiredRecordPlan(record string, desired ...string) routePlan {
	return routePlan{desired: desired, requiredRecord: record}
}

func assertSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
		return
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] == 0 {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
		seen[w]--
	}
}

func routePatterns(routes []map[string]any) []string {
	var patterns []string
	for _, r := range routes {
		patterns = append(patterns, r["pattern"].(string))
	}
	return patterns
}

type routeCase struct {
	name   string
	mock   *cfMock
	script string
	plan   routePlan

	wantErr         []string
	createdRoutes   []string
	repointedRoutes []string
	deletedRoutes   []string
	createdRecords  int
	deletedRecords  []string
	detachedDomains []string
	warnings        []string
}

func (tc routeCase) run(t *testing.T) {
	t.Helper()

	var warnings []string
	err := tc.mock.provider(t).reconcileWorkerRoutes(
		t.Context(),
		upload{accountID: "acct", scriptName: tc.script},
		tc.plan,
		func(s string) { warnings = append(warnings, s) },
	)

	switch {
	case len(tc.wantErr) > 0 && err == nil:
		t.Fatalf("reconcileWorkerRoutes err = nil, want a failure mentioning %v", tc.wantErr)
	case len(tc.wantErr) > 0:
		for _, want := range tc.wantErr {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		}
	case err != nil:
		t.Fatalf("reconcileWorkerRoutes: %v", err)
	}

	assertSet(t, "created routes", routePatterns(tc.mock.createdRoutes), tc.createdRoutes)
	assertSet(t, "repointed routes", tc.mock.repointedRoutes, tc.repointedRoutes)
	assertSet(t, "deleted routes", tc.mock.deletedRoutes, tc.deletedRoutes)
	assertSet(t, "deleted records", tc.mock.deletedRecords, tc.deletedRecords)
	assertSet(t, "detached custom domains", tc.mock.deletedCustomDomains, tc.detachedDomains)

	if len(tc.mock.createdRecords) != tc.createdRecords {
		t.Errorf("created records = %v, want %d", tc.mock.createdRecords, tc.createdRecords)
	}
	if len(warnings) != len(tc.warnings) {
		t.Fatalf("warnings = %v, want %d", warnings, len(tc.warnings))
	}
	for i, want := range tc.warnings {
		if !strings.Contains(warnings[i], want) {
			t.Errorf("warning %d = %q, want it to mention %q", i, warnings[i], want)
		}
	}
}

func TestReconcileWorkerRoutes(t *testing.T) {
	t.Parallel()

	t.Run("a wildcard hostname gets a proxied AAAA placeholder and a nil warn is tolerated", func(t *testing.T) {
		t.Parallel()

		m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
		up := upload{accountID: "acct", scriptName: "ocel-preview"}
		if err := m.provider(t).reconcileWorkerRoutes(t.Context(), up, prunedPlan("*.preview.app.com"), nil); err != nil {
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
		if rec["content"] != routeRecordContent {
			t.Errorf("record content = %v, want %q", rec["content"], routeRecordContent)
		}
		if rec["proxied"] != true {
			t.Errorf("record proxied = %v, want true", rec["proxied"])
		}
	})

	for _, tc := range []routeCase{
		{
			name:           "every desired domain is attached and given a placeholder",
			mock:           &cfMock{zoneID: "zone1", zoneName: "app.com"},
			script:         "ocel-prod",
			plan:           prunedPlan("app.com", "www.app.com"),
			createdRoutes:  []string{"app.com/*", "www.app.com/*"},
			createdRecords: 2,
		},
		{
			name: "a route and record already in place are left untouched",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
				},
				existingRecords: []map[string]any{
					{"id": "record1", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
			script:   "ocel-preview",
			plan:     prunedPlan("*.preview.app.com"),
			warnings: []string{"Advanced Certificate"},
		},
		{
			name: "a domain dropped from the plan loses its route and placeholder",
			mock: &cfMock{
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
			},
			script:         "ocel-prod",
			plan:           prunedPlan("app.com"),
			deletedRoutes:  []string{"stale"},
			deletedRecords: []string{"wwwrec"},
			createdRecords: 1,
		},
		{
			name: "a stem sweeps routes on the worker family below it",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "perapp", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shop-preview-web"},
				},
				existingRecords: []map[string]any{
					{"id": "perapprec", "name": "pr-1.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
			script:         "ocel-shop-preview",
			plan:           stemPlan("ocel-shop-preview", "*.preview.app.com"),
			createdRoutes:  []string{"*.preview.app.com/*"},
			deletedRoutes:  []string{"perapp"},
			deletedRecords: []string{"perapprec"},
			createdRecords: 1,
			warnings:       []string{"Advanced Certificate"},
		},
		{
			name: "a stem never reaches another worker family",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "sibling", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shopfoo-preview-web"},
					{"id": "other", "pattern": "pr-2.preview.app.com/*", "script": "ocel-other-preview"},
					{"id": "prod", "pattern": "app.com/*", "script": "ocel-shop-prod-web"},
					{"id": "lookalike", "pattern": "pr-3.preview.app.com/*", "script": "ocel-shop-previewer"},
				},
			},
			script:         "ocel-shop-preview",
			plan:           stemPlan("ocel-shop-preview", "*.preview.app.com"),
			createdRoutes:  []string{"*.preview.app.com/*"},
			createdRecords: 1,
			warnings:       []string{"Advanced Certificate"},
		},
		{
			name: "a stem never prunes a hostname the plan still wants",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "wildcard", "pattern": "*.preview.app.com/*", "script": "ocel-shop-preview-web"},
				},
			},
			script:          "ocel-shop-preview",
			plan:            stemPlan("ocel-shop-preview", "*.preview.app.com"),
			repointedRoutes: []string{"wildcard"},
			createdRecords:  1,
			warnings:        []string{"Advanced Certificate"},
		},
		{
			name: "without a stem the sweep reaches this script only",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "perapp", "pattern": "pr-1.preview.app.com/*", "script": "ocel-shop-preview-web"},
				},
			},
			script:         "ocel-shop-preview",
			plan:           prunedPlan("*.preview.app.com"),
			createdRoutes:  []string{"*.preview.app.com/*"},
			createdRecords: 1,
			warnings:       []string{"Advanced Certificate"},
		},
		{
			name: "without pruning a route this reconcile did not name survives",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "unnamed", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
				},
				existingRecords: []map[string]any{
					{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
			script:        "ocel-preview",
			plan:          requiredRecordPlan("*.preview.app.com", "pr-2-abc1234567.preview.app.com"),
			createdRoutes: []string{"pr-2-abc1234567.preview.app.com/*"},
			warnings:      []string{"Advanced Certificate"},
		},
		{
			name: "a required record present is not this deploy's to plant",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRecords: []map[string]any{
					{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
			script:        "ocel-preview",
			plan:          requiredRecordPlan("*.preview.app.com", "pr-1-abc1234567.preview.app.com"),
			createdRoutes: []string{"pr-1-abc1234567.preview.app.com/*"},
			warnings:      []string{"Advanced Certificate"},
		},
		{
			name:    "a required record missing fails and names the record to add",
			mock:    &cfMock{zoneID: "zone1", zoneName: "app.com"},
			script:  "ocel-preview",
			plan:    requiredRecordPlan("*.preview.app.com", "pr-1-abc1234567.preview.app.com"),
			wantErr: []string{"*.preview.app.com"},
		},
		{
			name: "a required record left unproxied fails and says so",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRecords: []map[string]any{
					{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": false},
				},
			},
			script:  "ocel-preview",
			plan:    requiredRecordPlan("*.preview.app.com", "pr-1-abc1234567.preview.app.com"),
			wantErr: []string{"proxied", "*.preview.app.com"},
		},
		{
			name: "a leftover custom domain is detached before the route is attached",
			mock: &cfMock{
				zoneID:                "zone1",
				zoneName:              "app.com",
				existingCustomDomains: []map[string]any{{"id": "cd1", "hostname": "app.com", "service": "ocel-prod"}},
			},
			script:          "ocel-prod",
			plan:            prunedPlan("app.com"),
			createdRoutes:   []string{"app.com/*"},
			createdRecords:  1,
			detachedDomains: []string{"cd1"},
		},
		{
			name:           "a hostname Universal SSL does not cover is warned about",
			mock:           &cfMock{zoneID: "zone1", zoneName: "app.com"},
			script:         "ocel-preview",
			plan:           prunedPlan("*.preview.app.com"),
			createdRoutes:  []string{"*.preview.app.com/*"},
			createdRecords: 1,
			warnings:       []string{"Advanced Certificate"},
		},
		{
			name: "a user's unproxied record is warned about, never overwritten",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRecords: []map[string]any{
					{"id": "user", "name": "app.com", "type": "A", "content": "203.0.113.1", "proxied": false},
				},
			},
			script:        "ocel-prod",
			plan:          prunedPlan("app.com"),
			createdRoutes: []string{"app.com/*"},
			warnings:      []string{"proxied"},
		},
		{
			name: "a TXT record at the hostname does not block the placeholder",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRecords: []map[string]any{
					{"id": "spf", "name": "app.com", "type": "TXT", "content": "v=spf1 -all", "proxied": false},
				},
			},
			script:         "ocel-prod",
			plan:           prunedPlan("app.com"),
			createdRoutes:  []string{"app.com/*"},
			createdRecords: 1,
		},
		{
			name: "a user's proxied address record is left alone without a warning",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRecords: []map[string]any{
					{"id": "user", "name": "app.com", "type": "A", "content": "203.0.113.1", "proxied": true},
				},
			},
			script:        "ocel-prod",
			plan:          prunedPlan("app.com"),
			createdRoutes: []string{"app.com/*"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.run(t)
		})
	}
}

func TestReconcileWorkerRoutesRequestBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		mock            *cfMock
		plan            routePlan
		zoneLists       int
		routeLists      int
		reconcilePasses int
	}{
		{
			name:       "many hostnames share one zone and route list",
			mock:       &cfMock{zoneID: "zone1", zoneName: "app.com"},
			plan:       prunedPlan("app.com", "www.app.com", "api.app.com"),
			zoneLists:  1,
			routeLists: 1,
		},
		{
			name:       "a required record costs no extra list",
			mock:       &cfMock{zoneID: "zone1", zoneName: "app.com", existingRecords: []map[string]any{{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true}}},
			plan:       requiredRecordPlan("*.preview.app.com", "pr-1.preview.app.com", "pr-2.preview.app.com"),
			zoneLists:  1,
			routeLists: 1,
		},
		{
			name:            "zones are read once, routes once per pass",
			mock:            &cfMock{zoneID: "zone1", zoneName: "app.com"},
			plan:            prunedPlan("app.com"),
			reconcilePasses: 3,
			zoneLists:       1,
			routeLists:      3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := tc.mock.provider(t)
			for range max(tc.reconcilePasses, 1) {
				if err := p.reconcileWorkerRoutes(t.Context(), upload{accountID: "acct", scriptName: "ocel-prod"}, tc.plan, nil); err != nil {
					t.Fatalf("reconcileWorkerRoutes: %v", err)
				}
			}
			if tc.mock.zoneLists != tc.zoneLists {
				t.Errorf("zone lists = %d, want %d", tc.mock.zoneLists, tc.zoneLists)
			}
			if tc.mock.routeLists != tc.routeLists {
				t.Errorf("route lists = %d, want %d", tc.mock.routeLists, tc.routeLists)
			}
		})
	}
}

func TestRouteOwnerRequestBudget(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)
	for _, host := range []string{"app.com", "www.app.com"} {
		if _, err := p.RouteOwner(t.Context(), RoutePattern(host)); err != nil {
			t.Fatalf("RouteOwner: %v", err)
		}
	}
	if m.zoneLists != 1 {
		t.Errorf("zone lists = %d, want 1: preflight reads the account's zones once", m.zoneLists)
	}
}

func TestRoutePattern(t *testing.T) {
	t.Parallel()

	t.Run("is the one spelling of a hostname as a route", func(t *testing.T) {
		t.Parallel()

		if got := RoutePattern("*.preview.app.com"); got != "*.preview.app.com/*" {
			t.Errorf("RoutePattern = %q, want *.preview.app.com/*", got)
		}
	})
}

func TestRouteOwner(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	for _, tc := range []struct {
		name    string
		mock    *cfMock
		pattern string
		want    string
		wantErr bool
	}{
		{
			name: "reports the script bound to the pattern",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-other-preview"},
				},
			},
			pattern: RoutePattern("*.preview.app.com"),
			want:    "ocel-other-preview",
		},
		{
			name:    "a pattern nothing holds is unclaimed",
			mock:    &cfMock{zoneID: "zone1", zoneName: "app.com"},
			pattern: RoutePattern("*.preview.app.com"),
		},
		{
			name: "an overlapping wildcard is not a match",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "*.app.com/*", "script": "ocel-other-preview"},
				},
			},
			pattern: RoutePattern("*.preview.app.com"),
		},
		{
			name:    "a hostname outside the account is an error",
			mock:    &cfMock{zoneID: "zone1", zoneName: "app.com"},
			pattern: RoutePattern("*.preview.elsewhere.com"),
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := tc.mock.provider(t).RouteOwner(t.Context(), tc.pattern)
			if tc.wantErr {
				if err == nil {
					t.Fatal("RouteOwner err = nil, want the unresolvable zone reported")
				}
				return
			}
			if err != nil {
				t.Fatalf("RouteOwner: %v", err)
			}
			if owner != tc.want {
				t.Errorf("owner = %q, want %q", owner, tc.want)
			}
			if n := len(tc.mock.createdRoutes) + len(tc.mock.createdRecords) + len(tc.mock.deletedRoutes); n != 0 {
				t.Error("RouteOwner changed the zone; it is read-only")
			}
		})
	}
}

func TestDetachRouteRecords(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		mock *cfMock
		want []string
	}{
		{
			name: "removes only the placeholders ocel planted",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
					{"id": "route2", "pattern": "other.app.com/*", "script": "someone-else"},
				},
				existingRecords: []map[string]any{
					{"id": "ours", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
			want: []string{"ours"},
		},
		{
			name: "leaves a user's own AAAA record standing",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "*.preview.app.com/*", "script": "ocel-preview"},
				},
				existingRecords: []map[string]any{
					{"id": "user", "name": "*.preview.app.com", "type": "AAAA", "content": "2606:4700::1", "proxied": true},
				},
			},
		},
		{
			name: "leaves a record no route of its own names",
			mock: &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "route1", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
					{"id": "route2", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "ocel-preview"},
				},
				existingRecords: []map[string]any{
					{"id": "base", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := tc.mock.provider(t)
			if err := p.detachRouteRecords(t.Context(), p.routeSnapshot(), "acct", "ocel-preview"); err != nil {
				t.Fatalf("detachRouteRecords: %v", err)
			}
			assertSet(t, "deleted records", tc.mock.deletedRecords, tc.want)
		})
	}
}

func TestCoveredByUniversalSSL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		host, zone string
		want       bool
	}{
		{"the apex is its own certificate's subject", "acme.com", "acme.com", true},
		{"one label below the apex", "www.acme.com", "acme.com", true},
		{"the wildcard one label below the apex", "*.acme.com", "acme.com", true},
		{"a wildcard two labels below the apex", "*.preview.acme.com", "acme.com", false},
		{"two labels below the apex", "a.b.acme.com", "acme.com", false},
		{"a hostname in another zone", "other.com", "acme.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := coveredByUniversalSSL(tc.host, tc.zone); got != tc.want {
				t.Errorf("coveredByUniversalSSL(%q, %q) = %v, want %v", tc.host, tc.zone, got, tc.want)
			}
		})
	}
}

func TestCanonicalDomainURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		domains []string
		want    string
	}{
		{"no domain has no canonical URL", nil, ""},
		{"a single plain hostname is the canonical URL", []string{"app.com"}, "https://app.com"},
		{"the first non-wildcard wins", []string{"*.app.com", "app.com"}, "https://app.com"},
		{"all wildcards falls back to the first", []string{"*.app.com"}, "https://*.app.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := canonicalDomainURL(tc.domains); got != tc.want {
				t.Errorf("canonicalDomainURL(%v) = %q, want %q", tc.domains, got, tc.want)
			}
		})
	}
}

func TestRouteBaseDomain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, host, want string
	}{
		{"a wildcard resolves through the hostname below it", "*.preview.acme.com", "preview.acme.com"},
		{"a plain hostname resolves through itself", "www.acme.com", "www.acme.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := routeBaseDomain(tc.host); got != tc.want {
				t.Errorf("routeBaseDomain(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
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
		{"an unrelated zone", "app.acme.com", "other.com", false},
		{"a zone that is only a suffix of the label", "app.acme.com", "me.com", false},
		{"a hostname that merely ends in the zone name", "notacme.com", "acme.com", false},
		{"a zone sharing the tail of a label", "app.acme.com", "cme.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := zoneOwns(tc.hostname, tc.zone); got != tc.want {
				t.Errorf("zoneOwns(%q, %q) = %v, want %v", tc.hostname, tc.zone, got, tc.want)
			}
		})
	}
}
