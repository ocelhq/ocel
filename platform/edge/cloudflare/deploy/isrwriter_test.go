package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
