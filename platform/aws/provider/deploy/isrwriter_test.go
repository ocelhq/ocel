package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const testPrefix = "prod/acme/web/BUILD1"

func writerAccess(endpoint string) ISRWriterAccess {
	return ISRWriterAccess{Endpoint: endpoint, BootstrapCred: "cred-1", Seed: "seed-1"}
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

func adoptISRWriter(t *testing.T, cfg Config) Config {
	t.Helper()
	srv, _ := fakeWriter(t, http.StatusNoContent)
	cfg.ISRWriterEndpoint = srv.URL
	cfg.ISRWriterBootstrapCred = "cred-1"
	cfg.ISRWriterSeed = "seed-1"
	return cfg
}

func TestResolveAppBuildsISRWriter(t *testing.T) {
	t.Run("refuses a writer and a store that disagree", func(t *testing.T) {
		base := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", StateTable: "state", Env: "prod"}

		storeOnly := base
		storeOnly.CacheStoreBucket = "isr"
		storeOnly.CacheStoreUploader = &fakeUploader{exists: map[string]bool{}}
		if err := checkISRWriterAgrees(providerkit.ClassProduction, storeOnly.objectStores(), storeOnly.isrWriter()); err == nil {
			t.Error("a cache store with no writer to write into it must fail the deploy")
		}

		writerOnly := adoptISRWriter(t, base)
		if err := checkISRWriterAgrees(providerkit.ClassProduction, writerOnly.objectStores(), writerOnly.isrWriter()); err == nil {
			t.Error("a writer with no adopted cache store must fail the deploy")
		}

		pre := providerkit.DeployPreflight{Plan: providerkit.DeployPlan{Slug: "shop", Class: providerkit.ClassProduction, Env: "prod"}}
		if err := newReleaser(fixed(storeOnly), &Realized{}, nil).Preflight(context.Background(), pre); err == nil {
			t.Error("a bootstrap that disagrees with itself must fail preflight, before a byte of this deploy is uploaded")
		}
	})

	t.Run("gives each app its own writer coordinates", func(t *testing.T) {
		t.Parallel()
		cfg := Config{
			AssetBucket:            "assets",
			StateTable:             "state",
			Env:                    "prod",
			CacheStoreBucket:       "isr",
			CacheStoreUploader:     &fakeUploader{exists: map[string]bool{}},
			ISRWriterEndpoint:      "https://writer.example",
			ISRWriterBootstrapCred: "cred-1",
			ISRWriterSeed:          "seed-1",
		}
		held := releasing(t, cfg)

		web := held.isrCache(isrPlan("web", "prod/proj/web/r1/isr"))
		admin := held.isrCache(isrPlan("admin", "prod/proj/admin/r1/isr"))
		again := releasing(t, cfg).isrCache(isrPlan("web", "prod/proj/web/r1/isr"))

		if want := "https://writer.example/prod/proj/web/r1/isr/entry"; web.WriterURL != want {
			t.Errorf("web WriterURL = %q, want %q", web.WriterURL, want)
		}
		if web.WriterSecret == admin.WriterSecret {
			t.Error("two apps in one deploy must not share a write secret")
		}
		if web.WriterSecret != again.WriterSecret {
			t.Error("a release must derive the same write secret on every call")
		}
	})

	t.Run("leaves writer coordinates unset without an adopted writer", func(t *testing.T) {
		t.Parallel()
		cfg := Config{AssetBucket: "assets", StateTable: "state", Env: "prod"}

		cache := releasing(t, cfg).isrCache(isrPlan("web", "prod/proj/web/r1/isr"))
		if cache.WriterURL != "" || cache.WriterSecret != "" {
			t.Errorf("writer coordinates = %+v, want unset", cache)
		}
	})
}

func isrPlan(app, prefix string) providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "proj", Class: providerkit.ClassProduction, Name: naming.AppStack("prod", app, releaseOf(deployedAs(testDeploymentID)))},
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:       app,
			Framework: frameworkNext,
			ISR:       &providerkit.ISRPlan{Prefix: prefix, TagNamespace: "tag:proj"},
		},
	}
}

func TestISRWriteSecret(t *testing.T) {
	t.Run("differs per prefix", func(t *testing.T) {
		t.Parallel()
		web := isrWriteSecret("seed-1", "prod/acme/web/B1")
		admin := isrWriteSecret("seed-1", "prod/acme/admin/B1")
		if web == admin {
			t.Error("two apps in one deploy must not share a write secret")
		}
	})

	t.Run("is stable", func(t *testing.T) {
		t.Parallel()
		web := isrWriteSecret("seed-1", "prod/acme/web/B1")
		if web != isrWriteSecret("seed-1", "prod/acme/web/B1") {
			t.Error("the same seed and prefix must derive the same secret on every call")
		}
	})

	t.Run("rotates with a fresh deploy seed", func(t *testing.T) {
		t.Parallel()
		web := isrWriteSecret("seed-1", "prod/acme/web/B1")
		if web == isrWriteSecret("seed-2", "prod/acme/web/B1") {
			t.Error("a fresh deploy seed must rotate the secret")
		}
	})

	t.Run("is not empty", func(t *testing.T) {
		t.Parallel()
		if isrWriteSecret("seed-1", "prod/acme/web/B1") == "" {
			t.Error("derived secret is empty")
		}
	})
}

func TestISRWriteSecretHash(t *testing.T) {
	t.Run("is the hex SHA256 the worker stores", func(t *testing.T) {
		t.Parallel()
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
	})
}

func TestInitializeISRWriter(t *testing.T) {
	t.Run("seeds only the hash under the bootstrap credential", func(t *testing.T) {
		srv, calls := fakeWriter(t, http.StatusNoContent)
		w := writerAccess(srv.URL)
		secret := isrWriteSecret(w.Seed, testPrefix)

		if err := initializeISRWriter(context.Background(), w, testPrefix, secret); err != nil {
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
	})
}

func TestRetireISRWriter(t *testing.T) {
	t.Run("destroys the build's instance", func(t *testing.T) {
		srv, calls := fakeWriter(t, http.StatusNoContent)

		if err := retireISRWriter(context.Background(), writerAccess(srv.URL), testPrefix); err != nil {
			t.Fatalf("retireISRWriter: %v", err)
		}
		if len(*calls) != 1 || (*calls)[0].path != "/"+testPrefix+"/destroy" {
			t.Fatalf("calls = %+v, want one POST to /%s/destroy", *calls, testPrefix)
		}
	})

	t.Run("reaches the worker without a deploy seed", func(t *testing.T) {
		srv, calls := fakeWriter(t, http.StatusNoContent)
		w := ISRWriterAccess{Endpoint: srv.URL, BootstrapCred: "cred-1"}

		if err := retireISRWriter(context.Background(), w, testPrefix); err != nil {
			t.Fatalf("retireISRWriter: %v", err)
		}
		if len(*calls) != 1 || (*calls)[0].path != "/"+testPrefix+"/destroy" {
			t.Fatalf("calls = %+v, want one POST to /%s/destroy", *calls, testPrefix)
		}
	})
}

func TestISRWriterRequest(t *testing.T) {
	t.Run("rejected call is an error", func(t *testing.T) {
		srv, _ := fakeWriter(t, http.StatusUnauthorized)

		err := initializeISRWriter(context.Background(), writerAccess(srv.URL), testPrefix, "s")
		if err == nil {
			t.Fatal("a 401 from the writer must not be swallowed")
		}
	})
}

func TestISRWriterCalls(t *testing.T) {
	t.Run("are no-ops when no writer was adopted", func(t *testing.T) {
		srv, calls := fakeWriter(t, http.StatusNoContent)
		for _, w := range []ISRWriterAccess{
			{},
			{Endpoint: srv.URL},
			{BootstrapCred: "cred-1"},
		} {
			if err := initializeISRWriter(context.Background(), w, testPrefix, "s"); err != nil {
				t.Fatalf("initializeISRWriter with %+v: %v", w, err)
			}
			if err := retireISRWriter(context.Background(), w, testPrefix); err != nil {
				t.Fatalf("retireISRWriter with %+v: %v", w, err)
			}
		}
		if len(*calls) != 0 {
			t.Errorf("calls = %+v, want none without adopted writer coordinates", *calls)
		}
	})
}

func TestISRWriterEnv(t *testing.T) {
	t.Run("is a plain env var pair or nothing", func(t *testing.T) {
		t.Parallel()
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
	})
}
