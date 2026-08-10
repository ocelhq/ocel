package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func doMetadataFromMultipart(t *testing.T, worker edge.Worker, do durableObjectWorker, deployedClasses []string) map[string]any {
	t.Helper()
	body, contentType, err := buildDurableObjectScriptMultipart(worker, do, deployedClasses)
	if err != nil {
		t.Fatalf("buildDurableObjectScriptMultipart: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "metadata" {
			continue
		}
		data, _ := io.ReadAll(part)
		var meta map[string]any
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		return meta
	}
	t.Fatal("no metadata part in multipart body")
	return nil
}

func testStoreWorker() edge.Worker {
	return edge.Worker{Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")}}
}

func TestBuildDurableObjectScriptMultipartDisablesObservability(t *testing.T) {
	t.Setenv(envObservability, "off")

	meta := doMetadataFromMultipart(t, testStoreWorker(), deploymentsStoreWorker, nil)
	obs, ok := meta["observability"].(map[string]any)
	if !ok {
		t.Fatalf("metadata has no observability object: %v", meta["observability"])
	}
	if obs["enabled"] != false {
		t.Errorf("observability.enabled = %v, want false", obs["enabled"])
	}
}

func TestStoreScriptNameFor(t *testing.T) {
	prod, err := storeScriptNameFor(edge.ClassProduction)
	if err != nil {
		t.Fatalf("storeScriptNameFor(production): %v", err)
	}
	preview, err := storeScriptNameFor(edge.ClassPreview)
	if err != nil {
		t.Fatalf("storeScriptNameFor(preview): %v", err)
	}
	if prod != sharedStoreScriptName || preview != previewStoreScriptName {
		t.Errorf("script names = (%q, %q), want (%q, %q)", prod, preview, sharedStoreScriptName, previewStoreScriptName)
	}
	if prod == preview {
		t.Error("production and preview deployments-store scripts must differ so their DO namespaces do not collide")
	}
	if _, err := storeScriptNameFor(edge.Class("nonsense")); err == nil {
		t.Error("storeScriptNameFor(unknown class) = nil error, want an error")
	}
}

func TestISRWriterScriptNameFor(t *testing.T) {
	prod, err := isrWriterScriptNameFor(edge.ClassProduction)
	if err != nil {
		t.Fatalf("isrWriterScriptNameFor(production): %v", err)
	}
	preview, err := isrWriterScriptNameFor(edge.ClassPreview)
	if err != nil {
		t.Fatalf("isrWriterScriptNameFor(preview): %v", err)
	}
	if prod != isrWriterScriptName || preview != previewISRWriterScriptName {
		t.Errorf("script names = (%q, %q), want (%q, %q)", prod, preview, isrWriterScriptName, previewISRWriterScriptName)
	}
	if prod == preview {
		t.Error("production and preview isr-writer scripts must differ so their DO namespaces do not collide")
	}
	if prod == sharedStoreScriptName || preview == previewStoreScriptName {
		t.Error("the isr-writer and deployments-store scripts must be distinct")
	}
	if _, err := isrWriterScriptNameFor(edge.Class("nonsense")); err == nil {
		t.Error("isrWriterScriptNameFor(unknown class) = nil error, want an error")
	}
}

func doBindings(t *testing.T, meta map[string]any) map[string]string {
	t.Helper()
	bindings, _ := meta["bindings"].([]any)
	found := map[string]string{}
	for _, b := range bindings {
		m, ok := b.(map[string]any)
		if !ok || m["type"] != "durable_object_namespace" {
			continue
		}
		name, _ := m["name"].(string)
		className, _ := m["class_name"].(string)
		found[name] = className
	}
	return found
}

func migrationSteps(t *testing.T, migrations map[string]any) [][]string {
	t.Helper()
	steps, ok := migrations["steps"].([]any)
	if !ok {
		t.Fatalf("migrations.steps = %v, want a list of steps", migrations["steps"])
	}
	out := [][]string{}
	for _, step := range steps {
		m, _ := step.(map[string]any)
		classes, _ := m["new_sqlite_classes"].([]any)
		names := []string{}
		for _, c := range classes {
			name, _ := c.(string)
			names = append(names, name)
		}
		out = append(out, names)
	}
	return out
}

func TestBuildDurableObjectScriptMultipart_BindsEveryClass(t *testing.T) {
	for _, do := range []durableObjectWorker{deploymentsStoreWorker, isrWriterWorker} {
		found := doBindings(t, doMetadataFromMultipart(t, testStoreWorker(), do, nil))
		if len(found) != len(do.classes) {
			t.Errorf("bound %d Durable Object classes, want %d: %v", len(found), len(do.classes), found)
		}
		for _, class := range do.classes {
			if found[class.binding] != class.className {
				t.Errorf("binding %s = %q, want %q", class.binding, found[class.binding], class.className)
			}
		}
	}
}

func TestBuildDurableObjectScriptMultipart_FreshBootstrapDeclaresTheWholeLog(t *testing.T) {
	meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, nil)
	migrations, ok := meta["migrations"].(map[string]any)
	if !ok {
		t.Fatalf("expected a migrations object on a fresh bootstrap, got %v", meta["migrations"])
	}
	if _, present := migrations["old_tag"]; present {
		t.Errorf("migrations.old_tag = %v, want none: there is no deployed tag to verify against", migrations["old_tag"])
	}
	if migrations["new_tag"] != "v2" {
		t.Errorf("migrations.new_tag = %v, want v2", migrations["new_tag"])
	}
	if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrDeploy"}, {"IsrSnapshot"}}) {
		t.Errorf("migrations.steps = %v, want the whole log", steps)
	}
}

func TestBuildDurableObjectScriptMultipart_BootstrappedAccountDeclaresWhatItLacks(t *testing.T) {
	meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, []string{"IsrDeploy"})
	migrations, ok := meta["migrations"].(map[string]any)
	if !ok {
		t.Fatalf("expected a migrations object for a script behind the log, got %v", meta["migrations"])
	}
	if _, present := migrations["old_tag"]; present {
		t.Errorf("migrations.old_tag = %v, want none: the API reports no tag to verify against", migrations["old_tag"])
	}
	if migrations["new_tag"] != "v2" {
		t.Errorf("migrations.new_tag = %v, want v2", migrations["new_tag"])
	}
	if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrSnapshot"}}) {
		t.Errorf("migrations.steps = %v, want only the class the script lacks", steps)
	}
}

func TestBuildDurableObjectScriptMultipart_UpToDateScriptDeclaresNoMigration(t *testing.T) {
	for _, do := range []durableObjectWorker{deploymentsStoreWorker, isrWriterWorker} {
		var deployed []string
		for _, step := range do.migrations {
			deployed = append(deployed, step.sqliteClasses...)
		}
		meta := doMetadataFromMultipart(t, testStoreWorker(), do, deployed)
		if _, present := meta["migrations"]; present {
			t.Errorf("expected no migrations on an up-to-date script, got %v", meta["migrations"])
		}
	}
}

func TestBuildDurableObjectScriptMultipart_UnknownDeployedClassIsRefused(t *testing.T) {
	if _, _, err := buildDurableObjectScriptMultipart(testStoreWorker(), isrWriterWorker, []string{"IsrDeploy", "IsrFuture"}); err == nil {
		t.Error("buildDurableObjectScriptMultipart(deployed IsrFuture) = nil error, want a refusal")
	}
}

func TestBuildDurableObjectScriptMultipart_CarriesTheNativeBucketBinding(t *testing.T) {
	worker := testStoreWorker()
	worker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: cacheStoreName(edge.ClassProduction)}

	meta := doMetadataFromMultipart(t, worker, isrWriterWorker, nil)
	bindings, _ := meta["bindings"].([]any)
	var found map[string]any
	for _, b := range bindings {
		if m, ok := b.(map[string]any); ok && m["type"] == "r2_bucket" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("no r2_bucket binding in %v", bindings)
	}
	if found["name"] != cacheStoreBinding || found["bucket_name"] != cacheStoreName(edge.ClassProduction) {
		t.Errorf("r2 binding = %v, want name %s bucket %s", found, cacheStoreBinding, cacheStoreName(edge.ClassProduction))
	}
}

const (
	storeBootstrapCred = "bootstrap-cred"
	storeOwnerToken    = "owner-token"
)

func fakeStoreServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	var (
		staged  []edge.DeploymentRecord
		history []edge.HistoryEntry
		version *string
	)
	owner, live := storeOwnerToken, secret
	if secret == "" {
		owner = ""
	}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if live == "" || r.Header.Get("Authorization") != "Bearer "+live {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /{slug}/initialize", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+storeBootstrapCred {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			OwnerToken string `json:"ownerToken"`
			Secret     string `json:"secret"`
			Force      bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OwnerToken == "" || body.Secret == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if owner == "" || body.Force {
			owner, live = body.OwnerToken, body.Secret
		}
		json.NewEncoder(w).Encode(map[string]string{"ownerToken": owner, "secret": live})
	})
	mux.HandleFunc("PUT /{slug}/staged", authed(func(w http.ResponseWriter, r *http.Request) {
		var rec edge.DeploymentRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		staged = append(staged, rec)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/promote", authed(func(w http.ResponseWriter, r *http.Request) {
		var p edge.Promotion
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		history = append([]edge.HistoryEntry{{Promotion: p, Active: true}}, history...)
		for i := range history[1:] {
			history[i+1].Active = false
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /{slug}/history", authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(history)
	}))
	mux.HandleFunc("POST /{slug}/prune", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			KeepN int `json:"keepN"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := edge.PruneResult{}
		for i, h := range history {
			if i < body.KeepN || h.Active {
				result.KeptPromotionIDs = append(result.KeptPromotionIDs, h.PromotionID)
			} else {
				result.RemovedPromotionIDs = append(result.RemovedPromotionIDs, h.PromotionID)
			}
		}
		json.NewEncoder(w).Encode(result)
	}))
	mux.HandleFunc("GET /{slug}/version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]*string{"version": version})
	}))
	mux.HandleFunc("PUT /{slug}/version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		version = &body.Version
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/destroy", authed(func(w http.ResponseWriter, r *http.Request) {
		staged, history, version = nil, nil, nil
		owner, live = "", ""
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testState(endpoint, secret string) edge.RootStackState {
	return edge.RootStackState{
		edge.RootStackKeySlug:       "acme-web",
		edge.RootStackKeyEndpoint:   endpoint,
		edge.RootStackKeySecret:     secret,
		edge.RootStackKeyOwnerToken: storeOwnerToken,
	}
}

func testSpec(endpoint, version string) edge.RootStackSpec {
	return edge.RootStackSpec{
		Slug:          "acme-web",
		StoreEndpoint: endpoint,
		BootstrapCred: storeBootstrapCred,
		Version:       version,
	}
}

func previewSpec(endpoint, version string) edge.RootStackSpec {
	spec := testSpec(endpoint, version)
	spec.GenericName = "ocel-preview"
	spec.Generic = testStoreWorker()
	spec.Domains = []string{"*.preview.app.com"}
	spec.PruneRoutes = true
	return spec
}

func previewZoneMock() *cfMock {
	return &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
}

func TestReconcileRootStack_UpToDateStillReconcilesTheRoute(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	ctx := context.Background()
	if err := p.putVersionStamp(ctx, store.URL, "acme-web", "s3cr3t", "v2"); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}

	prior := testState(store.URL, "s3cr3t")
	state, err := p.ReconcileRootStack(ctx, previewSpec(store.URL, "v2"), prior)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if !reflect.DeepEqual(state, prior) {
		t.Errorf("state = %v, want prior handed back unchanged (%v)", state, prior)
	}
	if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "*.preview.app.com/*" {
		t.Errorf("created routes = %v, want the project's wildcard route", m.createdRoutes)
	}
	if len(m.putScripts) != 0 {
		t.Errorf("uploaded scripts = %v, want none: the root stack already carries spec.Version", m.putScripts)
	}
}

func TestReconcileRootStack_PreviewPrunesEveryRouteButItsWildcard(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	m.existingRoutes = []map[string]any{
		{"id": "stale", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
		{"id": "other", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "someone-else"},
	}
	p := m.provider(t)

	if _, err := p.ReconcileRootStack(context.Background(), previewSpec(store.URL, "v2"), testState(store.URL, "s3cr3t")); err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "stale" {
		t.Errorf("deleted routes = %v, want only the stale pointer-exact route", m.deletedRoutes)
	}
}

func TestReconcileRootStack_PreviewPlantsItsOwnWildcardRecord(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	m.existingRecords = nil
	p := m.provider(t)

	if _, err := p.ReconcileRootStack(context.Background(), previewSpec(store.URL, "v2"), testState(store.URL, "s3cr3t")); err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "*.preview.app.com" {
		t.Errorf("created records = %v, want the proxied placeholder for *.preview.app.com", m.createdRecords)
	}
}

func TestReconcileRootStack_BehindVersionUploadsAndStamps(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	ctx := context.Background()

	state, err := p.ReconcileRootStack(ctx, previewSpec(store.URL, "v2"), testState(store.URL, "s3cr3t"))
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if len(m.putScripts) != 1 || m.putScripts[0] != "ocel-preview" {
		t.Errorf("uploaded scripts = %v, want [ocel-preview]", m.putScripts)
	}
	if len(m.createdRoutes) != 1 {
		t.Errorf("created routes = %v, want the project's wildcard route", m.createdRoutes)
	}
	version, _, err := p.getVersionStamp(ctx, store.URL, "acme-web", state[edge.RootStackKeySecret])
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	if version != "v2" {
		t.Errorf("version stamp = %q, want v2", version)
	}
}

func TestListDeployedWorkers_ReturnsTheStemsFamilyOnly(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingScripts: []string{
			"ocel-shop-preview",
			"ocel-shop-preview-web",
			"ocel-shop-previewer",
			"ocel-shopfoo-preview",
			"ocel-shop-prod-web",
			"ocel-other-preview",
		},
	}

	names, err := m.provider(t).ListDeployedWorkers(context.Background(), "ocel-shop-preview")
	if err != nil {
		t.Fatalf("ListDeployedWorkers: %v", err)
	}
	want := []string{"ocel-shop-preview", "ocel-shop-preview-web"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestListDeployedWorkers_NothingUnderTheStemIsNotAnError(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com", existingScripts: []string{"ocel-other-preview"}}

	names, err := m.provider(t).ListDeployedWorkers(context.Background(), "ocel-shop-preview")
	if err != nil {
		t.Fatalf("ListDeployedWorkers: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want none", names)
	}
}

func TestDestroyRootStack_EmptyListIsNoOp(t *testing.T) {
	t.Setenv(envAccountID, "acct-1")
	p := &provider{}
	if err := p.DestroyRootStack(context.Background(), nil); err != nil {
		t.Fatalf("DestroyRootStack(nil) err = %v, want nil", err)
	}
}

func TestDestroyRootStack_RequiresAccountID(t *testing.T) {
	t.Setenv(envAccountID, "")
	p := &provider{}
	if err := p.DestroyRootStack(context.Background(), []string{"ocel-proj-prod-web"}); err == nil {
		t.Fatal("DestroyRootStack without an account id err = nil, want an error")
	}
}

func TestPutStaged_RoundTrips(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	record := edge.DeploymentRecord{
		App: "web", Identity: "b1", FunctionURLs: map[string]string{"/": "https://fn"}, AssetPrefix: "b1", IsrPrefix: "prod/proj/web/b1", CreatedAt: 100,
	}
	if err := p.PutStaged(context.Background(), testState(srv.URL, "s3cr3t"), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
}

func TestPutStaged_WrongSecretIsUnauthorized(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	err := p.PutStaged(context.Background(), testState(srv.URL, "wrong"), edge.DeploymentRecord{App: "web", Identity: "b1"})
	if err == nil {
		t.Fatal("expected an error for the wrong write secret")
	}
}

func TestPromoteThenHistory_ReportsActivePromotion(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	state := testState(srv.URL, "s3cr3t")
	promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1000, Builds: map[string]string{"web": "b1"}}

	if err := p.Promote(context.Background(), state, promotion, ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	history, err := p.History(context.Background(), state, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %v, want 1 entry", history)
	}
	if history[0].PromotionID != "promo-1" || !history[0].Active {
		t.Errorf("history[0] = %+v, want promo-1 active", history[0])
	}
}

func TestDeletePromotionArtifacts_KeepsWindowAndPinsActive(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	state := testState(srv.URL, "s3cr3t")
	ctx := context.Background()

	for _, id := range []string{"p1", "p2", "p3"} {
		if err := p.Promote(ctx, state, edge.Promotion{PromotionID: id, Ts: 1, Builds: map[string]string{"web": id}}, ""); err != nil {
			t.Fatalf("Promote(%s): %v", id, err)
		}
	}

	result, err := p.DeletePromotionArtifacts(ctx, state, 1, "")
	if err != nil {
		t.Fatalf("DeletePromotionArtifacts: %v", err)
	}
	want := []string{"p2", "p1"}
	if len(result.RemovedPromotionIDs) != len(want) || result.RemovedPromotionIDs[0] != want[0] || result.RemovedPromotionIDs[1] != want[1] {
		t.Errorf("RemovedPromotionIDs = %v, want %v", result.RemovedPromotionIDs, want)
	}
}

func TestStoreOps_TransmitPointer(t *testing.T) {
	var (
		promoteBodies []map[string]any
		historyQuery  []string
		pruneBodies   []map[string]any
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{slug}/promote", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		promoteBodies = append(promoteBodies, b)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /{slug}/history", func(w http.ResponseWriter, r *http.Request) {
		historyQuery = append(historyQuery, r.URL.Query().Get("pointer"))
		json.NewEncoder(w).Encode([]edge.HistoryEntry{})
	})
	mux.HandleFunc("POST /{slug}/prune", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		pruneBodies = append(pruneBodies, b)
		json.NewEncoder(w).Encode(edge.PruneResult{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := &provider{}
	state := testState(srv.URL, "s3cr3t")
	ctx := context.Background()

	if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}}, "pr-42"); err != nil {
		t.Fatalf("Promote(preview): %v", err)
	}
	if _, err := p.History(ctx, state, "pr-42"); err != nil {
		t.Fatalf("History(preview): %v", err)
	}
	if _, err := p.DeletePromotionArtifacts(ctx, state, 3, "pr-42"); err != nil {
		t.Fatalf("Prune(preview): %v", err)
	}
	if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": "b2"}}, ""); err != nil {
		t.Fatalf("Promote(prod): %v", err)
	}
	if _, err := p.History(ctx, state, ""); err != nil {
		t.Fatalf("History(prod): %v", err)
	}
	if _, err := p.DeletePromotionArtifacts(ctx, state, 3, ""); err != nil {
		t.Fatalf("Prune(prod): %v", err)
	}

	if promoteBodies[0]["pointer"] != "pr-42" || promoteBodies[0]["promotionId"] != "p1" {
		t.Errorf("preview promote body = %v, want pointer pr-42 alongside the promotion", promoteBodies[0])
	}
	if _, ok := promoteBodies[1]["pointer"]; ok {
		t.Errorf("production promote body carried a pointer field: %v", promoteBodies[1])
	}
	if historyQuery[0] != "pr-42" || historyQuery[1] != "" {
		t.Errorf("history pointer queries = %v, want [pr-42 <empty>]", historyQuery)
	}
	if pruneBodies[0]["pointer"] != "pr-42" {
		t.Errorf("preview prune body = %v, want pointer pr-42", pruneBodies[0])
	}
	if _, ok := pruneBodies[1]["pointer"]; ok {
		t.Errorf("production prune body carried a pointer field: %v", pruneBodies[1])
	}
}

func TestRemovePointer_SendsThePointerAndReturnsReclaimTargets(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{slug}/remove-pointer", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(edge.PruneResult{
			RemovedPromotionIDs: []string{"promo-1"},
			RemovedRecordKeys:   []string{"record:web/b1"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := &provider{}
	result, err := p.RemovePointer(context.Background(), testState(srv.URL, "s3cr3t"), "pr-42")
	if err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if body["pointer"] != "pr-42" {
		t.Errorf("remove-pointer body = %v, want the pointer pr-42", body)
	}
	if len(result.RemovedRecordKeys) != 1 || result.RemovedRecordKeys[0] != "record:web/b1" {
		t.Errorf("result = %+v, want the removed record keys to reclaim", result)
	}
}

func TestStoreRequest_RetriesUntilTheStoreAnswers(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /{slug}/staged", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := &provider{}
	if err := p.PutStaged(context.Background(), testState(srv.URL, "s3cr3t"), edge.DeploymentRecord{App: "web"}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3: the two unavailable answers must have been retried", attempts)
	}
}

func TestStoreRequest_DoesNotRetryARejectedCredential(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	p := &provider{}
	if err := p.PutStaged(context.Background(), testState(srv.URL, "wrong"), edge.DeploymentRecord{App: "web"}); err == nil {
		t.Fatal("PutStaged err = nil, want the rejection surfaced")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestStoreRequest_CancelledContextStopsRetrying(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p := &provider{}
	if err := p.PutStaged(ctx, testState(srv.URL, "s3cr3t"), edge.DeploymentRecord{App: "web"}); err == nil {
		t.Fatal("PutStaged err = nil, want the failure surfaced")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: a cancelled context must not wait out the backoff", attempts)
	}
}

func TestGetVersionStamp_UnsetReadsEmpty(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	v, _, err := p.getVersionStamp(context.Background(), srv.URL, "acme-web", "s3cr3t")
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	if v != "" {
		t.Errorf("version = %q, want empty", v)
	}
}

func TestPutThenGetVersionStamp_RoundTrips(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", "s3cr3t", "v2"); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}
	v, _, err := p.getVersionStamp(ctx, srv.URL, "acme-web", "s3cr3t")
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	if v != "v2" {
		t.Errorf("version = %q, want v2", v)
	}
}

func TestStoreRequest_NoEndpointErrors(t *testing.T) {
	p := &provider{}
	err := p.PutStaged(context.Background(), edge.RootStackState{}, edge.DeploymentRecord{App: "web", Identity: "b1"})
	if err == nil {
		t.Fatal("expected an error when the root-stack state carries no endpoint")
	}
}

func TestMintSecret_UniqueAndNonEmpty(t *testing.T) {
	a, err := mintSecret()
	if err != nil {
		t.Fatalf("mintSecret: %v", err)
	}
	b, err := mintSecret()
	if err != nil {
		t.Fatalf("mintSecret: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty secrets")
	}
	if a == b {
		t.Fatal("two mints produced the same secret")
	}
}

func TestDestroyInstance_NoSecretIsNoOp(t *testing.T) {
	p := &provider{}
	if err := p.DestroyInstance(context.Background(), edge.RootStackState{}); err != nil {
		t.Fatalf("DestroyInstance(empty) err = %v, want nil", err)
	}
}

func TestDestroyInstance_WipesTheInstance(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	state := testState(srv.URL, "s3cr3t")
	if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}}, ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := p.DestroyInstance(ctx, state); err != nil {
		t.Fatalf("DestroyInstance: %v", err)
	}
	if _, err := p.History(ctx, state, ""); err == nil {
		t.Error("history after destroy: err = nil, want the wiped instance to reject the secret")
	}
}

func TestDestroyInstance_AlreadyWipedIsSuccess(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	state := testState(srv.URL, "s3cr3t")
	if err := p.DestroyInstance(ctx, state); err != nil {
		t.Fatalf("DestroyInstance: %v", err)
	}
	if err := p.DestroyInstance(ctx, state); err != nil {
		t.Fatalf("DestroyInstance on an already-wiped instance: err = %v, want nil", err)
	}
}

func TestEnsureInstance_ReseedsAnInstanceWipedByAFailedTeardown(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	state := testState(srv.URL, "s3cr3t")

	if err := p.DestroyInstance(ctx, state); err != nil {
		t.Fatalf("DestroyInstance: %v", err)
	}

	id, upToDate, err := p.ensureInstance(ctx, testSpec(srv.URL, "v2"), state)
	if err != nil {
		t.Fatalf("ensureInstance after a wipe: %v", err)
	}
	if upToDate {
		t.Error("upToDate = true, want false: a wiped instance carries no version stamp")
	}
	if id.secret == "" || id.ownerToken == "" {
		t.Fatalf("identity = %+v, want the freshly seeded pair the wiped instance now carries", id)
	}
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", id.secret, "v2"); err != nil {
		t.Fatalf("the re-seeded instance still rejects the project: %v", err)
	}
}

func TestEnsureInstance_AdoptsTheIdentityAnAlreadySeededInstanceReports(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()

	id, upToDate, err := p.ensureInstance(ctx, testSpec(srv.URL, "v2"), nil)
	if err != nil {
		t.Fatalf("ensureInstance: %v", err)
	}
	if upToDate {
		t.Error("upToDate = true, want false: this reconcile read no version stamp of its own")
	}
	if id.secret != "s3cr3t" || id.ownerToken != storeOwnerToken {
		t.Fatalf("identity = %+v, want the pair the instance already carries", id)
	}
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", id.secret, "v2"); err != nil {
		t.Fatalf("the adopted secret does not authenticate: %v", err)
	}
}

func TestReconcileRootStack_PersistsTheAdoptedIdentity(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)

	state, err := p.ReconcileRootStack(context.Background(), previewSpec(store.URL, "v3"), nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}
	if state[edge.RootStackKeySecret] != "s3cr3t" {
		t.Errorf("persisted secret = %q, want the instance's own", state[edge.RootStackKeySecret])
	}
	if state[edge.RootStackKeyOwnerToken] != storeOwnerToken {
		t.Errorf("persisted owner token = %q, want the instance's own", state[edge.RootStackKeyOwnerToken])
	}
}

func TestEnsureInstance_UpToDateVersionSkipsTheReconcile(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", "s3cr3t", "v2"); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}
	id, upToDate, err := p.ensureInstance(ctx, testSpec(srv.URL, "v2"), testState(srv.URL, "s3cr3t"))
	if err != nil {
		t.Fatalf("ensureInstance: %v", err)
	}
	if !upToDate {
		t.Error("upToDate = false, want true for a root stack already at spec.Version")
	}
	if id.secret != "s3cr3t" {
		t.Errorf("secret = %q, want the one already in state", id.secret)
	}
}

func TestEnsureInstance_SeedsAFirstReconcile(t *testing.T) {
	srv := fakeStoreServer(t, "")
	p := &provider{}
	ctx := context.Background()

	id, upToDate, err := p.ensureInstance(ctx, testSpec(srv.URL, "v1"), nil)
	if err != nil {
		t.Fatalf("ensureInstance with no prior state: %v", err)
	}
	if upToDate {
		t.Error("upToDate = true, want false for a project with no root stack yet")
	}
	if id.secret == "" || id.ownerToken == "" || id.secret == id.ownerToken {
		t.Fatalf("minted identity = %+v, want two distinct non-empty credentials", id)
	}
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", id.secret, "v1"); err != nil {
		t.Fatalf("the seeded instance rejects the minted secret: %v", err)
	}
}

func TestEnsureInstance_DoesNotReseedOnAStoreFailure(t *testing.T) {
	var initialized int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			initialized++
		}
		w.Header().Set("Retry-After-Ms", "1")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := &provider{}

	if _, _, err := p.ensureInstance(context.Background(), testSpec(srv.URL, "v2"), testState(srv.URL, "s3cr3t")); err == nil {
		t.Fatal("ensureInstance err = nil, want the store failure surfaced")
	}
	if initialized != 0 {
		t.Errorf("initialize calls = %d, want 0: a 500 is not a lost secret", initialized)
	}
}

func TestWithService_DoesNotMutateCallersWorker(t *testing.T) {
	worker := edge.Worker{Services: map[string]string{"EXISTING": "x"}}
	out := withService(worker, "DEPLOYMENTS", "ocel-proj-store")

	if _, ok := worker.Services["DEPLOYMENTS"]; ok {
		t.Error("withService mutated the caller's Worker.Services map")
	}
	if out.Services["DEPLOYMENTS"] != "ocel-proj-store" || out.Services["EXISTING"] != "x" {
		t.Errorf("out.Services = %v", out.Services)
	}
}

func TestWithSecret_DoesNotMutateCallersWorker(t *testing.T) {
	worker := edge.Worker{Secrets: map[string]string{"EXISTING": "1"}}
	out := withSecret(worker, "WRITE_SECRET", "s")

	if _, ok := worker.Secrets["WRITE_SECRET"]; ok {
		t.Error("withSecret mutated the caller's Worker.Secrets map")
	}
	if out.Secrets["WRITE_SECRET"] != "s" || out.Secrets["EXISTING"] != "1" {
		t.Errorf("out.Secrets = %v", out.Secrets)
	}
}

func TestGenericWorker_BindsTheAccountWorkersItReaches(t *testing.T) {
	spec := edge.RootStackSpec{
		Generic:             testStoreWorker(),
		StoreScriptName:     "ocel-deployments-store",
		ISRWriterScriptName: "ocel-isr-writer",
	}

	worker := genericWorker(spec, "acme-web")

	if worker.Services[genericStoreBinding] != "ocel-deployments-store" {
		t.Errorf("Services[%s] = %q", genericStoreBinding, worker.Services[genericStoreBinding])
	}
	if worker.Services[genericISRWriterBinding] != "ocel-isr-writer" {
		t.Errorf("Services[%s] = %q", genericISRWriterBinding, worker.Services[genericISRWriterBinding])
	}
	if worker.Vars[genericSlugBinding] != "acme-web" {
		t.Errorf("Vars[%s] = %q", genericSlugBinding, worker.Vars[genericSlugBinding])
	}
}

func TestGenericWorker_LeavesTheISRWriterUnboundWhenTheSubstrateOffersNone(t *testing.T) {
	spec := edge.RootStackSpec{Generic: testStoreWorker(), StoreScriptName: "ocel-deployments-store"}

	worker := genericWorker(spec, "acme-web")

	if _, bound := worker.Services[genericISRWriterBinding]; bound {
		t.Errorf("Services = %v, want no %s binding", worker.Services, genericISRWriterBinding)
	}
}
