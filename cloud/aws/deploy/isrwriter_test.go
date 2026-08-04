package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testPrefix = "prod/acme/web/BUILD1"

func writerConfig(endpoint string) Config {
	return Config{
		ISRWriterEndpoint:      endpoint,
		ISRWriterBootstrapCred: "cred-1",
		ISRWriterSeed:          "seed-1",
	}
}

// One recorded call to the fake writer worker.
type writerCall struct {
	method string
	path   string
	auth   string
	body   map[string]string
}

func fakeWriter(t *testing.T, status int) (*httptest.Server, *[]writerCall) {
	t.Helper()
	var calls []writerCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := writerCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.body)
		}
		calls = append(calls, call)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestISRWriteSecret_DiffersPerPrefixAndIsStable(t *testing.T) {
	web := isrWriteSecret("seed-1", "prod/acme/web/B1")
	admin := isrWriteSecret("seed-1", "prod/acme/admin/B1")
	if web == admin {
		t.Error("two apps in one deploy must not share a write secret")
	}
	if web != isrWriteSecret("seed-1", "prod/acme/web/B1") {
		t.Error("the same seed and prefix must derive the same secret on every call")
	}
	if web == isrWriteSecret("seed-2", "prod/acme/web/B1") {
		t.Error("a fresh deploy seed must rotate the secret")
	}
	if web == "" {
		t.Error("derived secret is empty")
	}
}

func TestISRWriteSecretHash_IsTheHexSHA256TheWorkerStores(t *testing.T) {
	// The worker accepts exactly 64 lowercase hex characters (isSecretHash in
	// workers/isr-writer/src/auth.ts), and never sees the plaintext.
	hash := isrWriteSecretHash("write-secret")
	if len(hash) != 64 {
		t.Fatalf("hash = %q, want 64 hex characters", hash)
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash = %q, want lowercase hex", hash)
		}
	}
	if hash == "write-secret" {
		t.Error("the plaintext secret must never be what is sent")
	}
}

func TestInitializeISRWriter_SeedsOnlyTheHashUnderTheBootstrapCredential(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)
	cfg := writerConfig(srv.URL)
	secret := isrWriteSecret(cfg.ISRWriterSeed, testPrefix)

	if err := initializeISRWriter(context.Background(), cfg, testPrefix, secret); err != nil {
		t.Fatalf("initializeISRWriter: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	got := (*calls)[0]
	if got.method != http.MethodPost || got.path != "/"+testPrefix+"/initialize" {
		t.Errorf("call = %s %s, want POST /%s/initialize", got.method, got.path, testPrefix)
	}
	if got.auth != "Bearer cred-1" {
		t.Errorf("authorization = %q, want the bootstrap credential", got.auth)
	}
	if got.body["secretHash"] != isrWriteSecretHash(secret) {
		t.Errorf("secretHash = %q, want the hash of the write secret", got.body["secretHash"])
	}
	if _, leaked := got.body["secret"]; leaked {
		t.Error("the plaintext write secret must never reach the worker")
	}
}

func TestRetireISRWriter_DestroysTheBuildsInstance(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)

	if err := retireISRWriter(context.Background(), writerConfig(srv.URL), testPrefix); err != nil {
		t.Fatalf("retireISRWriter: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/"+testPrefix+"/destroy" {
		t.Fatalf("calls = %+v, want one POST to /%s/destroy", *calls, testPrefix)
	}
}

func TestISRWriterRequest_RejectedCallIsAnError(t *testing.T) {
	srv, _ := fakeWriter(t, http.StatusUnauthorized)

	err := initializeISRWriter(context.Background(), writerConfig(srv.URL), testPrefix, "s")
	if err == nil {
		t.Fatal("a 401 from the writer must not be swallowed")
	}
}

func TestISRWriterCalls_AreNoOpsWhenNoWriterWasAdopted(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)
	for _, cfg := range []Config{
		{},
		{ISRWriterEndpoint: srv.URL},
		{ISRWriterBootstrapCred: "cred-1"},
	} {
		if err := initializeISRWriter(context.Background(), cfg, testPrefix, "s"); err != nil {
			t.Fatalf("initializeISRWriter with %+v: %v", cfg, err)
		}
		if err := retireISRWriter(context.Background(), cfg, testPrefix); err != nil {
			t.Fatalf("retireISRWriter with %+v: %v", cfg, err)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %+v, want none without adopted writer coordinates", *calls)
	}
}

// A prune mints no seed — it derives no secret and points no function at the
// writer — but destroy is authorized by the bootstrap credential alone, so a
// retirement must still reach the worker (epic decision 6d).
func TestRetireISRWriter_ReachesTheWorkerWithoutADeploySeed(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)
	cfg := Config{ISRWriterEndpoint: srv.URL, ISRWriterBootstrapCred: "cred-1"}

	if err := retireISRWriter(context.Background(), cfg, testPrefix); err != nil {
		t.Fatalf("retireISRWriter: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/"+testPrefix+"/destroy" {
		t.Fatalf("calls = %+v, want one POST to /%s/destroy", *calls, testPrefix)
	}
}

// The whole point of the writer is that a function reaches it with what the
// membrane already injected, and never with an SSM read on the cold path
// (epic decision 6a).
func TestISRWriterEnv_IsAPlainEnvVarPairOrNothing(t *testing.T) {
	with := isrConfig{
		Prefix:       testPrefix,
		WriterURL:    "https://writer.example/" + testPrefix + "/entry",
		WriterSecret: "write-secret",
	}.env()
	if with["OCEL_ISR_WRITER_URL"] != "https://writer.example/"+testPrefix+"/entry" {
		t.Errorf("OCEL_ISR_WRITER_URL = %q", with["OCEL_ISR_WRITER_URL"])
	}
	if with["OCEL_ISR_WRITER_SECRET"] != "write-secret" {
		t.Errorf("OCEL_ISR_WRITER_SECRET = %q", with["OCEL_ISR_WRITER_SECRET"])
	}

	// A half-configured writer would leave the handler with an address it
	// cannot authenticate against, so neither half is emitted alone.
	for _, cfg := range []isrConfig{
		{Prefix: testPrefix},
		{Prefix: testPrefix, WriterURL: "https://writer.example/x/entry"},
		{Prefix: testPrefix, WriterSecret: "write-secret"},
	} {
		env := cfg.env()
		if _, ok := env["OCEL_ISR_WRITER_URL"]; ok {
			t.Errorf("OCEL_ISR_WRITER_URL set for %+v", cfg)
		}
		if _, ok := env["OCEL_ISR_WRITER_SECRET"]; ok {
			t.Errorf("OCEL_ISR_WRITER_SECRET set for %+v", cfg)
		}
	}
}

// appCaches is called several times in one deploy and every call must agree, or
// the hash seeded into the writer and the secret injected into the function
// disagree and every entry write 401s.
func TestAppCaches_GivesEachAppItsOwnWriterCoordinates(t *testing.T) {
	cfg := Config{
		ArtifactRoot:           twoAppTree(t),
		AssetBucket:            "assets",
		StateTable:             "state",
		Env:                    "prod",
		ISRWriterEndpoint:      "https://writer.example",
		ISRWriterBootstrapCred: "cred-1",
		ISRWriterSeed:          "seed-1",
	}

	caches, err := appCaches(cfg, twoAppManifest())
	if err != nil {
		t.Fatalf("appCaches: %v", err)
	}
	again, err := appCaches(cfg, twoAppManifest())
	if err != nil {
		t.Fatalf("appCaches (second call): %v", err)
	}

	web, admin := caches["web"], caches["admin"]
	if want := "https://writer.example/prod/proj/web/WEB1/entry"; web.WriterURL != want {
		t.Errorf("web WriterURL = %q, want %q", web.WriterURL, want)
	}
	if web.WriterSecret == admin.WriterSecret {
		t.Error("two apps in one deploy must not share a write secret")
	}
	if web.WriterSecret != again["web"].WriterSecret {
		t.Error("appCaches must derive the same write secret on every call")
	}
}

func TestAppCaches_LeavesWriterCoordinatesUnsetWithoutAnAdoptedWriter(t *testing.T) {
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", StateTable: "state", Env: "prod"}

	caches, err := appCaches(cfg, twoAppManifest())
	if err != nil {
		t.Fatalf("appCaches: %v", err)
	}
	if caches["web"].WriterURL != "" || caches["web"].WriterSecret != "" {
		t.Errorf("writer coordinates = %+v, want unset", caches["web"])
	}
}
