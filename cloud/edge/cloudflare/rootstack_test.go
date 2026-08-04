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
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// doMetadataFromMultipart mirrors cloudflare_test.go's metadataFromMultipart,
// for buildDurableObjectScriptMultipart's body.
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
	// The two account-level workers must not collide with each other either:
	// one script name, one DO namespace.
	if prod == sharedStoreScriptName || preview == previewStoreScriptName {
		t.Error("the isr-writer and deployments-store scripts must be distinct")
	}
	if _, err := isrWriterScriptNameFor(edge.Class("nonsense")); err == nil {
		t.Error("isrWriterScriptNameFor(unknown class) = nil error, want an error")
	}
}

// doBindings returns every durable_object_namespace binding in the metadata,
// keyed by binding name.
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

// migrationSteps flattens migrations.steps to the sqlite classes each step
// creates, in order.
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

// A worker owns as many Durable Object classes as its wrangler.jsonc declares,
// and every one of them needs its binding: a class the script exports but the
// script metadata does not bind is a class no request can reach.
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

// An account bootstrapping for the first time carries no migration tag, so
// every step in the log has to be declared at once or the classes after the
// first are never created.
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

// ocelhq-wvag.4: the case a boolean "the script does not exist yet" could not
// express. An account that already bootstrapped the writer carries v1, so a new
// class reaches it only if the upload declares the steps v1 is missing —
// otherwise Cloudflare rejects the binding to a class it never created.
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

// Redeclaring an applied migration is at best redundant and at worst rejected,
// and every bootstrap after the last one re-uploads the same script.
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

// A deployed class this build has never heard of is a script ahead of the code
// uploading it. Guessing a migration path from there is how a class gets
// deleted; refusing costs a rollback and nothing else.
func TestBuildDurableObjectScriptMultipart_UnknownDeployedClassIsRefused(t *testing.T) {
	if _, _, err := buildDurableObjectScriptMultipart(testStoreWorker(), isrWriterWorker, []string{"IsrDeploy", "IsrFuture"}); err == nil {
		t.Error("buildDurableObjectScriptMultipart(deployed IsrFuture) = nil error, want a refusal")
	}
}

// The writer's whole justification is that a Lambda needs no R2 credentials of
// its own: the bucket reaches the worker as a native binding instead.
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

// storeBootstrapCred / storeOwnerToken are the account-level credential
// fakeStoreServer authorizes /initialize with (the worker's BOOTSTRAP_SECRET)
// and the owner token its instance starts out seeded under.
const (
	storeBootstrapCred = "bootstrap-cred"
	storeOwnerToken    = "owner-token"
)

// fakeStoreServer stands in for workers/deployments-store/src/index.ts's
// fetch() surface, close enough to exercise the Go-side HTTP client without
// any Cloudflare API: it checks the Bearer secret and serves /initialize,
// /staged, /promote, /history, /prune, /version-stamp and /destroy.
//
// Ownership is modelled as the real store does it (store.ts initialize): the
// instance starts seeded with secret under storeOwnerToken, /initialize rotates
// the secret for a matching owner and 409s for a different one, and /destroy
// clears the secret with the rest of the storage — so every later op on the
// wiped instance is a 401, exactly as it is in production.
func fakeStoreServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	var (
		staged  []edge.DeploymentRecord
		history []edge.HistoryEntry
		version *string
	)
	// An empty secret is an instance nobody has seeded yet, so it has no owner
	// either and the first /initialize claims it.
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
		if owner != "" && owner != body.OwnerToken && !body.Force {
			w.WriteHeader(http.StatusConflict)
			return
		}
		owner, live = body.OwnerToken, body.Secret
		w.WriteHeader(http.StatusNoContent)
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

// testSpec is the reconcile spec matching testState's project, addressing the
// same instance with the account-level bootstrap credential.
func testSpec(endpoint, version string) edge.RootStackSpec {
	return edge.RootStackSpec{
		Slug:          "acme-web",
		StoreEndpoint: endpoint,
		BootstrapCred: storeBootstrapCred,
		Version:       version,
	}
}

// previewSpec is testSpec shaped like a preview pointer's reconcile: one exact
// pointer hostname resolved by the shared base wildcard, and no pruning.
func previewSpec(endpoint, version, hostname string) edge.RootStackSpec {
	spec := testSpec(endpoint, version)
	spec.GenericName = "ocel-preview"
	spec.Generic = testStoreWorker()
	spec.Domains = []string{hostname}
	spec.RequiredRecord = "*.preview.app.com"
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

// ocelhq-5w3: the version stamp is keyed on the project but a worker route is
// per pointer, so a second pointer deploying against a root stack already at
// spec.Version still gets its own route, while uploading nothing.
func TestReconcileRootStack_UpToDateStillAttachesTheRoute(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	ctx := context.Background()
	if err := p.putVersionStamp(ctx, store.URL, "acme-web", "s3cr3t", "v2"); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}

	prior := testState(store.URL, "s3cr3t")
	state, err := p.ReconcileRootStack(ctx, previewSpec(store.URL, "v2", "pr-2-abc1234567.preview.app.com"), prior)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if !reflect.DeepEqual(state, prior) {
		t.Errorf("state = %v, want prior handed back unchanged (%v)", state, prior)
	}
	if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "pr-2-abc1234567.preview.app.com/*" {
		t.Errorf("created routes = %v, want this pointer's own route", m.createdRoutes)
	}
	if len(m.putScripts) != 0 {
		t.Errorf("uploaded scripts = %v, want none: the root stack already carries spec.Version", m.putScripts)
	}
}

// A root stack behind spec.Version still does the whole job: upload the script,
// attach the route, and stamp the version it now carries.
func TestReconcileRootStack_BehindVersionUploadsAndStamps(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	ctx := context.Background()

	state, err := p.ReconcileRootStack(ctx, previewSpec(store.URL, "v2", "pr-1-abc1234567.preview.app.com"), testState(store.URL, "s3cr3t"))
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if len(m.putScripts) != 1 || m.putScripts[0] != "ocel-preview" {
		t.Errorf("uploaded scripts = %v, want [ocel-preview]", m.putScripts)
	}
	if len(m.createdRoutes) != 1 {
		t.Errorf("created routes = %v, want the pointer's route", m.createdRoutes)
	}
	version, _, err := p.getVersionStamp(ctx, store.URL, "acme-web", state[edge.RootStackKeySecret])
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	if version != "v2" {
		t.Errorf("version stamp = %q, want v2", version)
	}
}

// Per-pointer teardown: the shared script, its sibling pointers' routes and the
// base wildcard record all keep serving.
func TestRemoveRoute_DeletesOnlyTheNamedRoute(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := previewZoneMock()
	m.existingRoutes = []map[string]any{
		{"id": "gone", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
		{"id": "sibling", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "ocel-preview"},
		{"id": "other", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "someone-else"},
	}
	p := m.provider(t)

	if err := p.RemoveRoute(context.Background(), "ocel-preview", "pr-1-abc1234567.preview.app.com"); err != nil {
		t.Fatalf("RemoveRoute: %v", err)
	}

	if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "gone" {
		t.Errorf("deleted routes = %v, want [gone]", m.deletedRoutes)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted records = %v, want none: the base wildcard is shared by every pointer", m.deletedRecords)
	}
}

// A route that is already gone is not an error, so a teardown that failed
// half-way resumes on a re-run.
func TestRemoveRoute_AlreadyGoneIsSuccess(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := previewZoneMock()
	p := m.provider(t)

	if err := p.RemoveRoute(context.Background(), "ocel-preview", "pr-1-abc1234567.preview.app.com"); err != nil {
		t.Fatalf("RemoveRoute on a missing route: err = %v, want nil", err)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want none", m.deletedRoutes)
	}
}

func TestRemoveRoute_RequiresAccountID(t *testing.T) {
	t.Setenv(envAccountID, "")
	p := &provider{}
	if err := p.RemoveRoute(context.Background(), "ocel-preview", "pr-1.preview.app.com"); err == nil {
		t.Fatal("RemoveRoute without an account id err = nil, want an error")
	}
}

func TestDestroyRootStack_EmptyListIsNoOp(t *testing.T) {
	t.Setenv(envAccountID, "acct-1")
	p := &provider{}
	// No workers to remove must not reach the Cloudflare client (p.client is nil
	// here) — an empty teardown is a clean no-op.
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

// TestStoreOps_TransmitPointer proves the Go host sends the pointer the store
// scopes promote/history/prune by: as a sibling field of the /promote body, a
// ?pointer= query on /history, and a field of the /prune body. An empty pointer
// is omitted so the store applies its reserved production default.
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

	// Preview pointer: transmitted on all three.
	if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}}, "pr-42"); err != nil {
		t.Fatalf("Promote(preview): %v", err)
	}
	if _, err := p.History(ctx, state, "pr-42"); err != nil {
		t.Fatalf("History(preview): %v", err)
	}
	if _, err := p.DeletePromotionArtifacts(ctx, state, 3, "pr-42"); err != nil {
		t.Fatalf("Prune(preview): %v", err)
	}
	// Production default: pointer omitted from the promote/prune bodies and the
	// history query.
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
		json.NewEncoder(w).Encode(edge.PointerRemoval{
			PruneResult: edge.PruneResult{
				RemovedPromotionIDs: []string{"promo-1"},
				RemovedRecordKeys:   []string{"record:web/b1"},
			},
			RemainingPointers: 2,
			RemovedRoutes:     []edge.RemovedRoute{{App: "web", Hostname: "pr-42-abc1234567.preview.test"}},
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
	if result.RemainingPointers != 2 {
		t.Errorf("remaining pointers = %d, want 2", result.RemainingPointers)
	}
	want := edge.RemovedRoute{App: "web", Hostname: "pr-42-abc1234567.preview.test"}
	if len(result.RemovedRoutes) != 1 || result.RemovedRoutes[0] != want {
		t.Errorf("removed routes = %+v, want %+v", result.RemovedRoutes, want)
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
	// No secret in state means the project never deployed to production; wiping
	// its instance must not reach the store (srv-less) — a clean no-op.
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
	// The wipe takes the secret with it, so the instance no longer answers to
	// the state that named it.
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
	// Wiping deletes the secret /destroy authenticates with, so a re-run always
	// meets a 401. Reporting that as a failure would strand a teardown that
	// failed after the wipe: its state is only forgotten once this reports the
	// instance gone.
	if err := p.DestroyInstance(ctx, state); err != nil {
		t.Fatalf("DestroyInstance on an already-wiped instance: err = %v, want nil", err)
	}
}

func TestEnsureInstance_ReseedsAnInstanceWipedByAFailedTeardown(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	ctx := context.Background()
	state := testState(srv.URL, "s3cr3t")

	// A teardown wiped the instance and then failed, leaving the state naming it
	// in place — the deploy that follows must recover, not fail forever.
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
	if id.ownerToken != storeOwnerToken {
		t.Errorf("ownerToken = %q, want the project's own %q", id.ownerToken, storeOwnerToken)
	}
	if err := p.putVersionStamp(ctx, srv.URL, "acme-web", id.secret, "v2"); err != nil {
		t.Fatalf("the re-seeded instance still rejects the project: %v", err)
	}
}

func TestEnsureInstance_RefusesAnInstanceOwnedByAnotherProject(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	state := testState(srv.URL, "stale-secret")
	state[edge.RootStackKeyOwnerToken] = "another-projects-owner-token"

	if _, _, err := p.ensureInstance(context.Background(), testSpec(srv.URL, "v2"), state); err == nil {
		t.Fatal("ensureInstance err = nil, want a refusal: the slug belongs to another project")
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
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := &provider{}

	// Only a rejected credential means the instance lost our secret. Anything
	// else is a store we could not read, and re-seeding on it would rotate the
	// secret of a healthy instance.
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

// The generic worker reaches two shared account workers, and neither is
// something its own assembly can name: only the root stack knows which script
// each substrate class provisioned.
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

// An upload naming a script that does not exist is refused outright, so a
// substrate whose bootstrap predates the ISR writer binds nothing and serves
// on: its builds record invalidations and replicate none, exactly as they did
// before the edge published at all.
func TestGenericWorker_LeavesTheISRWriterUnboundWhenTheSubstrateOffersNone(t *testing.T) {
	spec := edge.RootStackSpec{Generic: testStoreWorker(), StoreScriptName: "ocel-deployments-store"}

	worker := genericWorker(spec, "acme-web")

	if _, bound := worker.Services[genericISRWriterBinding]; bound {
		t.Errorf("Services = %v, want no %s binding", worker.Services, genericISRWriterBinding)
	}
}
