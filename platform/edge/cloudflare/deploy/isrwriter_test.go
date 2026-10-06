package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testPrefix = "prod/acme/web/BUILD1"

func writerAccess(endpoint string) ISRWriter {
	return ISRWriter{Endpoint: endpoint, BootstrapCredential: "cred-1", Seed: "seed-1"}
}

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

func TestAnISRWriteSecretDiffersPerPrefixAndRotatesWithTheSeed(t *testing.T) {
	t.Run("differs per prefix", func(t *testing.T) {
		t.Parallel()
		web := DeriveISRWriteSecret("seed-1", "prod/acme/web/B1")
		admin := DeriveISRWriteSecret("seed-1", "prod/acme/admin/B1")
		if web == admin {
			t.Error("two apps in one deploy must not share a write secret")
		}
	})

	t.Run("is stable", func(t *testing.T) {
		t.Parallel()
		web := DeriveISRWriteSecret("seed-1", "prod/acme/web/B1")
		if web != DeriveISRWriteSecret("seed-1", "prod/acme/web/B1") {
			t.Error("the same seed and prefix must derive the same secret on every call")
		}
	})

	t.Run("rotates with a fresh deploy seed", func(t *testing.T) {
		t.Parallel()
		web := DeriveISRWriteSecret("seed-1", "prod/acme/web/B1")
		if web == DeriveISRWriteSecret("seed-2", "prod/acme/web/B1") {
			t.Error("a fresh deploy seed must rotate the secret")
		}
	})

	t.Run("is not empty", func(t *testing.T) {
		t.Parallel()
		if DeriveISRWriteSecret("seed-1", "prod/acme/web/B1") == "" {
			t.Error("derived secret is empty")
		}
	})
}

func TestAnISRWriteSecretHashIsTheHexSHA256TheWorkerStores(t *testing.T) {
	t.Run("is the hex SHA256 the worker stores", func(t *testing.T) {
		t.Parallel()
		hash := isrWriteSecretHash("write-secret")
		if len(hash) != 64 {
			t.Fatalf("hash = %q, want 64 hex characters", hash)
		}
		for _, c := range hash {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Fatalf("hash = %q, want lowercase hex", hash)
			}
		}
		if hash == "write-secret" {
			t.Error("the plaintext secret must never be what is sent")
		}
	})
}

func TestInitializingTheISRWriterSeedsOnlyTheHashUnderTheBootstrapCredential(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)
	w := writerAccess(srv.URL)
	secret := DeriveISRWriteSecret(w.Seed, testPrefix)

	if err := w.Initialize(context.Background(), testPrefix); err != nil {
		t.Fatalf("Initialize: %v", err)
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

func TestInitializingAnISRWriterWithNoSeedSendsNothing(t *testing.T) {
	srv, calls := fakeWriter(t, http.StatusNoContent)
	w := ISRWriter{Endpoint: srv.URL, BootstrapCredential: "cred-1"}

	if err := w.Initialize(context.Background(), testPrefix); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %+v, want none without a seed", *calls)
	}
}

func TestTheISRWriterRefusesARejectedCallAndKeepsItsOwnConnection(t *testing.T) {
	t.Run("rejected call is an error", func(t *testing.T) {
		srv, _ := fakeWriter(t, http.StatusUnauthorized)

		if err := writerAccess(srv.URL).Initialize(context.Background(), testPrefix); err == nil {
			t.Fatal("a 401 from the writer must not be swallowed")
		}
	})

	t.Run("keeps its connection when the default transport drops idle ones", func(t *testing.T) {
		var dialed atomic.Int32
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				dialed.Add(1)
			}
		}
		srv.Start()
		t.Cleanup(srv.Close)

		for i := range 3 {
			if err := writerAccess(srv.URL).Initialize(context.Background(), testPrefix); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		}
		if got := dialed.Load(); got != 1 {
			t.Errorf("writer saw %d connections, want 1: another client closing the default transport's idle connections reached the writer's", got)
		}
	})
}

func TestAnISRWriterThatNeverAnswersFailsInitializeWithinTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	previous := isrWriterTimeout
	isrWriterTimeout = 100 * time.Millisecond
	t.Cleanup(func() { isrWriterTimeout = previous })

	done := make(chan error, 1)
	go func() { done <- writerAccess(srv.URL).Initialize(context.Background(), testPrefix) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Initialize returned nil for a writer that never answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Initialize blocked past the isr writer timeout")
	}
}

func TestTheISRWriterCallsNothingWithoutAdoptedCoordinates(t *testing.T) {
	t.Run("are no-ops when no writer was adopted", func(t *testing.T) {
		srv, calls := fakeWriter(t, http.StatusNoContent)
		for _, w := range []ISRWriter{
			{},
			{Endpoint: srv.URL},
			{BootstrapCredential: "cred-1"},
		} {
			if err := w.Initialize(context.Background(), testPrefix); err != nil {
				t.Fatalf("Initialize with %+v: %v", w, err)
			}
		}
		if len(*calls) != 0 {
			t.Errorf("calls = %+v, want none without adopted writer coordinates", *calls)
		}
	})
}

func TestPuttingAnEntrySendsItUnderTheDeploymentsWriteSecret(t *testing.T) {
	var got struct {
		method, path, key, auth, contentType, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.path, got.key = r.Method, r.URL.Path, r.URL.Query().Get("key")
		got.auth, got.contentType, got.body = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	if err := writerAccess(srv.URL).PutEntry(context.Background(), testPrefix, "blog/a b", []byte(`{"lastModified":1}`)); err != nil {
		t.Fatalf("PutEntry = %v", err)
	}

	if got.method != http.MethodPut || got.path != "/"+testPrefix+"/entry" || got.key != "blog/a b" {
		t.Errorf("request = %s %s?key=%s, want PUT /%s/entry?key=blog/a b", got.method, got.path, got.key, testPrefix)
	}
	if want := "Bearer " + DeriveISRWriteSecret("seed-1", testPrefix); got.auth != want {
		t.Errorf("Authorization = %q, want the deployment's write secret, never the bootstrap credential", got.auth)
	}
	if got.contentType != "application/json" || got.body != `{"lastModified":1}` {
		t.Errorf("content type %q and body %q, want the entry as JSON", got.contentType, got.body)
	}
}

func TestPuttingAnEntryRetriesAThrottledWrite(t *testing.T) {
	previous := isrWriterBackoff
	isrWriterBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { isrWriterBackoff = previous })
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	if err := writerAccess(srv.URL).PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`)); err != nil {
		t.Fatalf("PutEntry = %v, want the third attempt to land", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("%d attempts, want 3", got)
	}

	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(always.Close)
	attempts.Store(0)
	if err := writerAccess(always.URL).PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`)); err == nil {
		t.Error("PutEntry = nil though the writer throttled every attempt")
	}
	if got := attempts.Load(); got != 4 {
		t.Errorf("%d attempts, want the first and three retries", got)
	}
}

func TestPuttingAnEntryRetriesAServerErrorAndATransportFailure(t *testing.T) {
	previous := isrWriterBackoff
	isrWriterBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { isrWriterBackoff = previous })
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch attempts.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	if err := writerAccess(srv.URL).PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`)); err != nil {
		t.Fatalf("PutEntry = %v, want the third attempt to land after a 503 and a dropped connection", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("%d attempts, want 3", got)
	}
}

func TestPuttingAnEntryDoesNotRetryAClientError(t *testing.T) {
	previous := isrWriterBackoff
	isrWriterBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { isrWriterBackoff = previous })
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	if err := writerAccess(srv.URL).PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`)); err == nil {
		t.Error("PutEntry = nil though the writer answered 403")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("%d attempts, want 1: a 403 never succeeds on retry", got)
	}
}

func TestPuttingAnEntryWithoutAnAdoptedWriterIsRefused(t *testing.T) {
	for name, writer := range map[string]ISRWriter{
		"no endpoint": {BootstrapCredential: "c", Seed: "s"},
		"no seed":     {Endpoint: "https://w.example", BootstrapCredential: "c"},
	} {
		err := writer.PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`))
		if err == nil || !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("%s: PutEntry = %v, want a refusal that says to re-run bootstrap", name, err)
		}
	}
}

func TestPuttingAnEntryTheWriterRejectedNamesTheKeyAndNotTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	err := writerAccess(srv.URL).PutEntry(context.Background(), testPrefix, "blog", []byte(`{}`))
	if err == nil {
		t.Fatal("PutEntry = nil though the writer answered 403")
	}
	for _, want := range []string{testPrefix, "blog", "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), DeriveISRWriteSecret("seed-1", testPrefix)) || strings.Contains(err.Error(), "seed-1") {
		t.Errorf("error %q holds the write secret", err)
	}
}

func scriptedWriter(t *testing.T, statuses ...int) (*httptest.Server, *[]writerCall) {
	t.Helper()
	var calls []writerCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, writerCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")})
		status := statuses[min(len(calls), len(statuses))-1]
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestDestroyingAnISRPrefixCallsTheWriterUntilItAnswersEmptied(t *testing.T) {
	srv, calls := scriptedWriter(t, http.StatusAccepted, http.StatusAccepted, http.StatusNoContent)

	if err := writerAccess(srv.URL).Destroy(context.Background(), "/"+testPrefix+"/"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(*calls) != 3 {
		t.Fatalf("calls = %d, want 3: 202 means objects remain", len(*calls))
	}
	for _, call := range *calls {
		if call.method != http.MethodPost || call.path != "/"+testPrefix+"/destroy" || call.auth != "Bearer cred-1" {
			t.Errorf("call = %+v, want POST /%s/destroy under the bootstrap credential", call, testPrefix)
		}
	}
}

func TestISRWriterDestroyRetriesAWriterThatRanOutOfBudget(t *testing.T) {
	srv, calls := scriptedWriter(t, http.StatusServiceUnavailable, http.StatusNoContent)

	if err := writerAccess(srv.URL).Destroy(context.Background(), testPrefix); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(*calls) != 2 {
		t.Errorf("calls = %d, want the failed one retried once", len(*calls))
	}
}

func TestDestroyingAnISRPrefixNeedsNoSeed(t *testing.T) {
	srv, calls := scriptedWriter(t, http.StatusNoContent)
	w := ISRWriter{Endpoint: srv.URL, BootstrapCredential: "cred-1"}

	if err := w.Destroy(context.Background(), testPrefix); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(*calls) != 1 {
		t.Errorf("calls = %d, want the destroy to go out without a seed", len(*calls))
	}
}

func TestDestroyingAnISRPrefixWithoutAWriterSendsNothing(t *testing.T) {
	srv, calls := scriptedWriter(t, http.StatusNoContent)
	for _, w := range []ISRWriter{{}, {Endpoint: srv.URL}, {BootstrapCredential: "cred-1"}} {
		if err := w.Destroy(context.Background(), testPrefix); err != nil {
			t.Errorf("Destroy(%+v) = %v, want nothing to do", w, err)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %+v, want none", *calls)
	}
}

func TestAnISRWriterThatRefusesADestroyIsAnError(t *testing.T) {
	srv, _ := scriptedWriter(t, http.StatusUnauthorized)

	err := writerAccess(srv.URL).Destroy(context.Background(), testPrefix)

	if err == nil || !strings.Contains(err.Error(), testPrefix) || !strings.Contains(err.Error(), "401") {
		t.Errorf("Destroy = %v, want an error naming the prefix and the status", err)
	}
}

func TestAnISRWriterThatNeverEmptiesAPrefixIsAnError(t *testing.T) {
	previous := isrWriterDestroyCalls
	isrWriterDestroyCalls = 3
	t.Cleanup(func() { isrWriterDestroyCalls = previous })
	srv, calls := scriptedWriter(t, http.StatusAccepted)

	err := writerAccess(srv.URL).Destroy(context.Background(), testPrefix)

	if err == nil || !strings.Contains(err.Error(), "prune again") || len(*calls) != 3 {
		t.Errorf("Destroy = %v after %d calls, want an error telling the caller to prune again after 3", err, len(*calls))
	}
}
