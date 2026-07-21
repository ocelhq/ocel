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
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// storeMetadataFromMultipart mirrors cloudflare_test.go's
// metadataFromMultipart, for buildStoreScriptMultipart's body.
func storeMetadataFromMultipart(t *testing.T, worker edge.Worker, migrate bool) map[string]any {
	t.Helper()
	body, contentType, err := buildStoreScriptMultipart(worker, migrate)
	if err != nil {
		t.Fatalf("buildStoreScriptMultipart: %v", err)
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

func TestBuildStoreScriptMultipart_BindsItsOwnDurableObjectClass(t *testing.T) {
	meta := storeMetadataFromMultipart(t, testStoreWorker(), false)
	bindings, _ := meta["bindings"].([]any)
	var found map[string]any
	for _, b := range bindings {
		if m, ok := b.(map[string]any); ok && m["type"] == "durable_object_namespace" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("no durable_object_namespace binding in %v", bindings)
	}
	if found["name"] != "DEPLOYMENTS_DO" || found["class_name"] != "DeploymentsStore" {
		t.Errorf("DO binding = %v, want name DEPLOYMENTS_DO class_name DeploymentsStore", found)
	}
}

func TestBuildStoreScriptMultipart_DeclaresMigrationOnlyWhenMigrateTrue(t *testing.T) {
	fresh := storeMetadataFromMultipart(t, testStoreWorker(), true)
	migrations, ok := fresh["migrations"].(map[string]any)
	if !ok {
		t.Fatalf("expected a migrations object on a fresh deploy, got %v", fresh["migrations"])
	}
	if migrations["tag"] != "v1" {
		t.Errorf("migrations.tag = %v, want v1", migrations["tag"])
	}
	classes, _ := migrations["new_sqlite_classes"].([]any)
	if len(classes) != 1 || classes[0] != "DeploymentsStore" {
		t.Errorf("migrations.new_sqlite_classes = %v, want [DeploymentsStore]", classes)
	}

	notFresh := storeMetadataFromMultipart(t, testStoreWorker(), false)
	if _, present := notFresh["migrations"]; present {
		t.Errorf("expected no migrations on a non-first reconcile, got %v", notFresh["migrations"])
	}
}

// fakeStoreServer stands in for workers/deployments-store/src/index.ts's
// fetch() surface, close enough to exercise the Go-side HTTP client without
// any Cloudflare API: it checks the Bearer secret and serves /staged,
// /promote, /history, /prune and /version-stamp.
func fakeStoreServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	var (
		staged  []edge.DeploymentRecord
		history []edge.HistoryEntry
		version *string
	)
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("PUT /staged", authed(func(w http.ResponseWriter, r *http.Request) {
		var rec edge.DeploymentRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		staged = append(staged, rec)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /promote", authed(func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /history", authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(history)
	}))
	mux.HandleFunc("POST /prune", authed(func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]*string{"version": version})
	}))
	mux.HandleFunc("PUT /version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testState(endpoint, secret string) edge.RootStackState {
	return edge.RootStackState{
		edge.RootStackKeyEndpoint:    endpoint,
		edge.RootStackKeyWriteSecret: secret,
	}
}

func TestPutStaged_RoundTrips(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	record := edge.DeploymentRecord{
		App: "web", BuildID: "b1", FunctionURLs: map[string]string{"/": "https://fn"}, TagNamespace: "ns", AssetPrefix: "b1", CreatedAt: 100,
	}
	if err := p.PutStaged(context.Background(), testState(srv.URL, "s3cr3t"), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
}

func TestPutStaged_WrongSecretIsUnauthorized(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	err := p.PutStaged(context.Background(), testState(srv.URL, "wrong"), edge.DeploymentRecord{App: "web", BuildID: "b1"})
	if err == nil {
		t.Fatal("expected an error for the wrong write secret")
	}
}

func TestPromoteThenHistory_ReportsActivePromotion(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	state := testState(srv.URL, "s3cr3t")
	promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1000, Builds: map[string]string{"web": "b1"}}

	if err := p.Promote(context.Background(), state, promotion); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	history, err := p.History(context.Background(), state)
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
		if err := p.Promote(ctx, state, edge.Promotion{PromotionID: id, Ts: 1, Builds: map[string]string{"web": id}}); err != nil {
			t.Fatalf("Promote(%s): %v", id, err)
		}
	}

	result, err := p.DeletePromotionArtifacts(ctx, state, 1)
	if err != nil {
		t.Fatalf("DeletePromotionArtifacts: %v", err)
	}
	want := []string{"p2", "p1"}
	if len(result.RemovedPromotionIDs) != len(want) || result.RemovedPromotionIDs[0] != want[0] || result.RemovedPromotionIDs[1] != want[1] {
		t.Errorf("RemovedPromotionIDs = %v, want %v", result.RemovedPromotionIDs, want)
	}
}

func TestGetVersionStamp_UnsetReadsEmpty(t *testing.T) {
	srv := fakeStoreServer(t, "s3cr3t")
	p := &provider{}
	v, err := p.getVersionStamp(context.Background(), srv.URL, "s3cr3t")
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
	if err := p.putVersionStamp(ctx, srv.URL, "s3cr3t", "v2"); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}
	v, err := p.getVersionStamp(ctx, srv.URL, "s3cr3t")
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	if v != "v2" {
		t.Errorf("version = %q, want v2", v)
	}
}

func TestStoreRequest_NoEndpointErrors(t *testing.T) {
	p := &provider{}
	err := p.PutStaged(context.Background(), edge.RootStackState{}, edge.DeploymentRecord{App: "web", BuildID: "b1"})
	if err == nil {
		t.Fatal("expected an error when the root-stack state carries no endpoint")
	}
}

func TestMintWriteSecret_UniqueAndNonEmpty(t *testing.T) {
	a, err := mintWriteSecret()
	if err != nil {
		t.Fatalf("mintWriteSecret: %v", err)
	}
	b, err := mintWriteSecret()
	if err != nil {
		t.Fatalf("mintWriteSecret: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty secrets")
	}
	if a == b {
		t.Fatal("two mints produced the same secret")
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
