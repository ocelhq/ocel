package cloudflare

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const frontedHostname = "api.app.com"

func frontedSpec(endpoint string) edge.StackSpec {
	spec := testSpec(endpoint, "v2")
	spec.Fronted = []string{frontedHostname}
	return spec
}

func cachingProvider(t *testing.T, m *cfMock) *cloudflare {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	p := m.provider(t)
	p.options.Cache = true
	return p
}

func lastUpload(t *testing.T, m *cfMock, script string) map[string]any {
	t.Helper()
	put, found := m.putBodies[script]
	if !found {
		t.Fatalf("uploaded scripts %v, want %s", m.putScripts, script)
	}
	return putMetadata(put)
}

func TestAnEdgeThatCachesNothingFrontsNoHost(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "forwarded", "pattern": frontedHostname + "/*", "script": ""}}
	t.Setenv(envAccountID, "acct")
	p := m.provider(t)
	prior := testState(store.URL, "s3cr3t")
	prior.Records = []edge.Record{{Name: frontedHostname, Type: edge.RecordTypeCNAME, Value: albAddress, Proxied: true}}

	state, err := reconcileState(t, p, frontedSpec(store.URL), prior)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if script, named := m.existingRoutes[0]["script"]; named && script != nil && script != "" {
		t.Errorf("%s/* runs %v, want no worker on a host an edge left uncached forwards", frontedHostname, script)
	}
	if own := ownOf(t, state); len(own.Fronted) != 0 {
		t.Errorf("the stack keeps fronted hosts %v, want none", own.Fronted)
	}
}

func TestTheEdgeFrontsItsOriginsExactlyWhenItCaches(t *testing.T) {
	for _, caching := range []bool{true, false} {
		p := newCloudflare("ns", Origin{}, Options{Cache: caching})
		if got := p.Facts().FrontsOrigins; got != caching {
			t.Errorf("with cache %v FrontsOrigins = %v, want %v", caching, got, caching)
		}
	}
}

func TestACachingEdgeRunsItsEntryWorkerOnAForwardedHostItFronts(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "forwarded", "pattern": frontedHostname + "/*", "script": ""}}
	p := cachingProvider(t, m)
	spec := frontedSpec(store.URL)
	prior := testState(store.URL, "s3cr3t")
	prior.Records = []edge.Record{{Name: frontedHostname, Type: edge.RecordTypeCNAME, Value: albAddress, Proxied: true}}

	if _, err := reconcileState(t, p, spec, prior); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(m.createdRoutes) != 0 || len(m.deletedRoutes) != 0 {
		t.Errorf("created routes %v and deleted %v, want the forwarded host's route repointed in place", m.createdRoutes, m.deletedRoutes)
	}
	if got := m.existingRoutes[0]["script"]; got != entryOf(t, spec).name {
		t.Errorf("%s/* runs %v, want the entry worker in front of the host's origin", frontedHostname, got)
	}
}

func TestACachingEdgeLeavesAScriptlessRouteItDidNotForward(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "theirs", "pattern": frontedHostname + "/*", "script": ""}}
	p := cachingProvider(t, m)

	if _, err := reconcileState(t, p, frontedSpec(store.URL), testState(store.URL, "s3cr3t")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if script, named := m.existingRoutes[0]["script"]; named && script != nil && script != "" {
		t.Errorf("%s/* runs %v, want the route left as it is until this stack forwards the host", frontedHostname, script)
	}
}

func TestAnEdgeThatStopsCachingForwardsAHostItFrontedWithNoWorker(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := zoneMock()
	t.Setenv(envAccountID, "acct")
	p := m.provider(t)
	spec := frontedSpec(store.URL)
	entry := entryOf(t, spec).name
	m.existingRoutes = []map[string]any{{"id": "fronted", "pattern": frontedHostname + "/*", "script": entry}}
	prior := testState(store.URL, "s3cr3t")
	prior.Private = edge.Own(private{EntryWorker: entry, Fronted: []string{frontedHostname}, FrontedBy: entry})

	if _, err := reconcileState(t, p, spec, prior); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if script, named := m.existingRoutes[0]["script"]; named && script != nil && script != "" {
		t.Errorf("%s/* runs %v, want no worker once the edge caches nothing", frontedHostname, script)
	}
}

func TestACachingEdgeForwardsANewlyBoundHostItFrontsThroughItsEntryWorker(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierProduction)
	s.p.options.Cache = true
	s.own.Fronted = []string{"api.app.com"}
	s.own.FrontedBy = s.own.EntryWorker

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.existingRoutes) != 1 || m.existingRoutes[0]["pattern"] != "api.app.com/*" || m.existingRoutes[0]["script"] != domainEntryScript {
		t.Errorf("the zone holds routes %v, want api.app.com/* running %s in front of the origin", m.existingRoutes, domainEntryScript)
	}
	if len(m.createdRecords) != 1 || m.createdRecords[0]["content"] != albAddress {
		t.Errorf("created records %v, want the host still forwarded to %s", m.createdRecords, albAddress)
	}
}

func TestAForwardedHostTheEdgeDoesNotFrontRunsNoWorkerEvenWhenItCaches(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierProduction)
	s.p.options.Cache = true

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	for _, route := range m.createdRoutes {
		if script, named := route["script"]; named && script != "" {
			t.Errorf("created route %v, want no worker on a host the edge was not told it fronts", route)
		}
	}
}

func TestUnbindingAFrontedHostTakesItsRouteOff(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierProduction)
	s.p.options.Cache = true
	s.own.Fronted = []string{"api.app.com"}
	s.own.FrontedBy = s.own.EntryWorker
	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if err := s.UnbindDomain(t.Context(), "api.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if len(m.existingRoutes) != 0 {
		t.Errorf("the zone holds routes %v, want the fronted host's route gone", m.existingRoutes)
	}
}

func TestACachingEdgeFrontsAPreviewHostOfAFrontedAppItAlreadyRuns(t *testing.T) {
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "fronted", "pattern": "api-pr-7.app.com/*", "script": domainEntryScript}}
	s := originStack(t, m, environment.TierPreview)
	s.p.options.Cache = true
	s.own.Fronted = []string{"api-pr-7.app.com"}
	s.own.FrontedBy = s.own.EntryWorker

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api-pr-7.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.existingRoutes) != 1 || m.existingRoutes[0]["script"] != domainEntryScript {
		t.Errorf("the zone holds routes %v, want the preview host still run by %s", m.existingRoutes, domainEntryScript)
	}
}

func TestACachingEdgeFrontsAPreviewHostOfAFrontedApp(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierPreview)
	s.p.options.Cache = true
	s.own.Fronted = []string{"api-pr-7.app.com"}
	s.own.FrontedBy = s.own.EntryWorker

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api-pr-7.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.existingRoutes) != 1 || m.existingRoutes[0]["script"] != domainEntryScript {
		t.Errorf("the zone holds routes %v, want the preview host run by %s, as production is", m.existingRoutes, domainEntryScript)
	}
}

func uploadedVars(meta map[string]any) map[string]string {
	vars := map[string]string{}
	for _, binding := range bindingsByType(meta, "plain_text") {
		vars[binding["name"].(string)] = binding["text"].(string)
	}
	return vars
}

func TestACachingEdgeTellsItsWorkerToCacheTheContainersItsOwnRouterServes(t *testing.T) {
	for _, caching := range []bool{true, false} {
		store := fakeStoreServer(t, "s3cr3t")
		m := zoneMock()
		t.Setenv(envAccountID, "acct")
		p := m.provider(t)
		p.options.Cache = caching
		spec := testSpec(store.URL, "v2")

		if _, err := reconcileState(t, p, spec, testState(store.URL, "s3cr3t")); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		got, set := uploadedVars(lastUpload(t, m, entryOf(t, spec).name))[cachesContainersBinding]
		if caching != (set && got == "1") {
			t.Errorf("with cache %v the worker's %s = %q (set %v), want it set to 1 exactly when the edge caches", caching, cachesContainersBinding, got, set)
		}
	}
}

const globalPreviewHost = "web-ab12cd.preview.app.com"

func globalPreviewFrontedSpec(endpoint string) edge.StackSpec {
	spec := pruneOnlySpec(endpoint, "v2")
	spec.GlobalPreview = "preview.app.com"
	spec.Fronted = []string{globalPreviewHost}
	return spec
}

func TestACachingEdgeFrontsAGlobalPreviewsForwardedHostWithTheSharedPreviewEntry(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	m.existingRoutes = []map[string]any{{"id": "forwarded", "pattern": globalPreviewHost + "/*", "script": ""}}
	p := cachingProvider(t, m)
	prior := testState(store.URL, "s3cr3t")
	prior.Tier = environment.TierPreview
	prior.Records = []edge.Record{{Name: globalPreviewHost, Type: edge.RecordTypeCNAME, Value: albAddress, Proxied: true}}

	state, err := reconcileState(t, p, globalPreviewFrontedSpec(store.URL), prior)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := m.existingRoutes[0]["script"]; got != previewEntryScriptName {
		t.Errorf("%s/* runs %v, want the shared preview entry in front of the host's origin", globalPreviewHost, got)
	}
	if own := ownOf(t, state); own.EntryWorker != "" || !slices.Equal(own.Fronted, []string{globalPreviewHost}) {
		t.Errorf("the stack keeps entry worker %q and fronted hosts %v, want no entry worker of its own and the host it fronts", own.EntryWorker, own.Fronted)
	}
}

func TestAGlobalPreviewHostBoundBehindACachingEdgeRunsTheSharedPreviewEntry(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierPreview)
	s.p.options.Cache = true
	s.own.Fronted = []string{"web-ab12cd.app.com"}
	s.own.FrontedBy = previewEntryScriptName

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "web-ab12cd.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.existingRoutes) != 1 || m.existingRoutes[0]["script"] != previewEntryScriptName {
		t.Errorf("the zone holds routes %v, want web-ab12cd.app.com/* running %s", m.existingRoutes, previewEntryScriptName)
	}
}

func TestTheSharedPreviewEntryOfACachingEdgeCachesContainersAndReadsTheObjectsItKeeps(t *testing.T) {
	m := zoneMock()
	p := cachingProvider(t, m)
	p.KeepsObjects = true
	installKeptBootstrap(t, m, environment.TierPreview)

	if _, err := p.ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}

	meta := lastUpload(t, m, previewEntryScriptName)
	if vars := bindingsByType(meta, "plain_text"); !slices.ContainsFunc(vars, func(b map[string]any) bool {
		return b["name"] == cachesContainersBinding && b["text"] == "1"
	}) {
		t.Errorf("the shared preview entry binds %v, want %s set", vars, cachesContainersBinding)
	}
	if buckets := bindingsByType(meta, "r2_bucket"); len(buckets) != 1 || buckets[0]["name"] != entryObjectsBinding || buckets[0]["bucket_name"] != keptBucketName(t, environment.TierPreview) {
		t.Errorf("the shared preview entry binds buckets %v, want %s bound to the preview tier's kept objects", buckets, entryObjectsBinding)
	}
}
