package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const testPrefix = "prod/acme/web/BUILD1"

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
		storeOnly.CacheStoreObjects = &fakeArtifactStore{exists: map[string]bool{}}
		if err := checkISRWriterAgrees(environment.TierProduction, storeOnly.objectStores(), storeOnly.isrWriter()); err == nil {
			t.Error("a cache store with no writer to write into it must fail the deploy")
		}

		writerOnly := adoptISRWriter(t, base)
		if err := checkISRWriterAgrees(environment.TierProduction, writerOnly.objectStores(), writerOnly.isrWriter()); err == nil {
			t.Error("a writer with no adopted cache store must fail the deploy")
		}

		pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"}}
		if err := newStacks(fixed(storeOnly), &Realized{}, nil).Preflight(context.Background(), pre); err == nil {
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
			CacheStoreObjects:      &fakeArtifactStore{exists: map[string]bool{}},
			ISRWriterEndpoint:      "https://writer.example",
			ISRWriterBootstrapCred: "cred-1",
			ISRWriterSeed:          "seed-1",
		}
		release := releasing(t, cfg)

		web := release.isrCache(isrSpec("web", "prod/proj/web/r1/isr"))
		admin := release.isrCache(isrSpec("admin", "prod/proj/admin/r1/isr"))
		again := releasing(t, cfg).isrCache(isrSpec("web", "prod/proj/web/r1/isr"))

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

		cache := releasing(t, cfg).isrCache(isrSpec("web", "prod/proj/web/r1/isr"))
		if cache.WriterURL != "" || cache.WriterSecret != "" {
			t.Errorf("writer coordinates = %+v, want unset", cache)
		}
	})
}

func isrSpec(app, prefix string) provider.StackSpec {
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "proj", Tier: environment.TierProduction, Name: naming.AppStack("prod", app, deployedAs(testDeploymentID).Release())},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       app,
			Framework: buildoutput.FrameworkNext,
			ISR:       &provider.ISRSpec{Prefix: prefix, TagNamespace: "tag:proj"},
		},
	}
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
