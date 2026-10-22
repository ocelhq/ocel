package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func testModule() workerModule {
	return workerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")}
}

func metadataFromMultipart(t *testing.T, worker workerScript, assetsJWT string) map[string]any {
	t.Helper()
	body, contentType, err := buildScriptMultipart(worker, assetsJWT)
	if err != nil {
		t.Fatalf("buildScriptMultipart: %v", err)
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

func bindingsByType(meta map[string]any, typ string) []map[string]any {
	var found []map[string]any
	bindings, _ := meta["bindings"].([]any)
	for _, b := range bindings {
		if m, ok := b.(map[string]any); ok && m["type"] == typ {
			found = append(found, m)
		}
	}
	return found
}

func hasAssetBinding(meta map[string]any) bool {
	return len(bindingsByType(meta, "assets")) > 0
}

func TestObservability(t *testing.T) {
	t.Run("a worker reports logs and traces by default", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule()}, "")
		obs, ok := meta["observability"].(map[string]any)
		if !ok {
			t.Fatalf("metadata has no observability object: %v", meta["observability"])
		}
		if obs["enabled"] != true {
			t.Errorf("observability.enabled = %v, want true", obs["enabled"])
		}
		logs, ok := obs["logs"].(map[string]any)
		if !ok || logs["enabled"] != true {
			t.Errorf("observability.logs not enabled: %v", obs["logs"])
		}
		traces, ok := obs["traces"].(map[string]any)
		if !ok || traces["enabled"] != true {
			t.Errorf("observability.traces not enabled: %v", obs["traces"])
		}
	})

	t.Run("switching it off strips every sampling knob", func(t *testing.T) {
		t.Setenv(envObservability, "off")

		meta := metadataFromMultipart(t, workerScript{Main: testModule()}, "")
		obs, ok := meta["observability"].(map[string]any)
		if !ok {
			t.Fatalf("metadata has no observability object: %v", meta["observability"])
		}
		if obs["enabled"] != false {
			t.Errorf("observability.enabled = %v, want false", obs["enabled"])
		}
		for _, key := range []string{"logs", "traces", "head_sampling_rate"} {
			if _, present := obs[key]; present {
				t.Errorf("observability.%s is set on a disabled worker: %v", key, obs[key])
			}
		}
	})
}

func TestScriptBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		worker workerScript
		typ    string
		want   []map[string]string
	}{
		{
			name:   "a secret becomes a secret_text binding containing its value",
			worker: workerScript{Main: testModule(), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"}},
			typ:    "secret_text",
			want:   []map[string]string{{"name": "OCEL_EDGE_SECRET_KEY", "text": "shh"}},
		},
		{
			name:   "a var becomes a plain_text binding",
			worker: workerScript{Main: testModule(), Variables: map[string]string{"OCEL_EDGE_ACCESS_KEY_ID": "AKIA"}},
			typ:    "plain_text",
			want:   []map[string]string{{"name": "OCEL_EDGE_ACCESS_KEY_ID", "text": "AKIA"}},
		},
		{
			name:   "a secret is never also emitted as plain text",
			worker: workerScript{Main: testModule(), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"}},
			typ:    "plain_text",
			want:   nil,
		},
		{
			name:   "a service maps to a service binding",
			worker: workerScript{Main: testModule(), Services: map[string]string{"RELEASES": "ocel-proj-store"}},
			typ:    "service",
			want:   []map[string]string{{"name": "RELEASES", "service": "ocel-proj-store"}},
		},
		{
			name:   "a loader binding maps to a worker_loader binding",
			worker: workerScript{Main: testModule(), LoaderBinding: "LOADER"},
			typ:    "worker_loader",
			want:   []map[string]string{{"name": "LOADER"}},
		},
		{
			name:   "no loader binding emits no worker_loader binding",
			worker: workerScript{Main: testModule()},
			typ:    "worker_loader",
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bindingsByType(metadataFromMultipart(t, tc.worker, ""), tc.typ)
			if len(got) != len(tc.want) {
				t.Fatalf("%s bindings = %v, want %d", tc.typ, got, len(tc.want))
			}
			for i, want := range tc.want {
				for k, v := range want {
					if got[i][k] != v {
						t.Errorf("%s binding %d %s = %v, want %q", tc.typ, i, k, got[i][k], v)
					}
				}
			}
		})
	}

	t.Run("a worker with only variables emits no other binding", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{
			Main:      testModule(),
			Variables: map[string]string{"FUNCTION_URLS": "{}"},
		}, "")
		for _, typ := range []string{"worker_loader", "service", "secret_text", "assets"} {
			if got := bindingsByType(meta, typ); len(got) != 0 {
				t.Errorf("%s bindings = %v, want none", typ, got)
			}
		}
		if got := len(bindingsByType(meta, "plain_text")); got != 1 {
			t.Errorf("plain_text bindings = %d, want the worker's variables unchanged", got)
		}
	})
}

func TestBuildScriptMultipart(t *testing.T) {
	t.Run("the assets binding and metadata are gated on a completion JWT", func(t *testing.T) {
		worker := workerScript{
			AssetBinding: "ASSETS",
			Main:         testModule(),
			Variables:    map[string]string{"FUNCTION_URLS": "{}"},
			Assets:       []workerAsset{{Path: "/a.svg", Content: []byte("a")}},
		}

		noJWT := metadataFromMultipart(t, worker, "")
		if _, ok := noJWT["assets"]; ok {
			t.Error("assets metadata must be absent without a completion JWT")
		}
		if hasAssetBinding(noJWT) {
			t.Error("assets binding must be absent without a completion JWT")
		}

		withJWT := metadataFromMultipart(t, worker, "completion-token")
		if _, ok := withJWT["assets"]; !ok {
			t.Error("assets metadata must be present with a completion JWT")
		}
		if !hasAssetBinding(withJWT) {
			t.Error("assets binding must be present with a completion JWT")
		}
	})

	t.Run("the main module and its siblings are uploaded as their own parts", func(t *testing.T) {
		worker := workerScript{
			Main:    testModule(),
			Modules: []workerModule{{Name: "next-route-table.json", ContentType: "text/plain", Content: []byte(`{"buildId":"b"}`)}},
		}

		body, contentType, err := buildScriptMultipart(worker, "")
		if err != nil {
			t.Fatalf("buildScriptMultipart: %v", err)
		}
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("parse content type: %v", err)
		}

		byName := map[string]string{}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			data, _ := io.ReadAll(part)
			byName[part.FormName()] = string(data)
		}

		if got := byName["index.js"]; got != "export default {}" {
			t.Errorf("index.js part = %q", got)
		}
		if got := byName["next-route-table.json"]; got != `{"buildId":"b"}` {
			t.Errorf("next-route-table.json part = %q", got)
		}
	})
}

func TestBindCodeLoader(t *testing.T) {
	t.Run("a bundle with no loader still gets the binding beside its store", func(t *testing.T) {
		composed := bindCodeLoader(withService(workerScript{Main: testModule()}, "RELEASES", "ocel-proj-store"))
		meta := metadataFromMultipart(t, composed, "")

		loaders := bindingsByType(meta, "worker_loader")
		if len(loaders) != 1 {
			t.Fatalf("got %d worker_loader bindings, want 1", len(loaders))
		}
		if loaders[0]["name"] != codeLoaderBinding {
			t.Errorf("worker_loader binding name = %v, want %q", loaders[0]["name"], codeLoaderBinding)
		}
		if got := len(bindingsByType(meta, "service")); got != 1 {
			t.Errorf("service bindings = %d, want the store's binding to survive", got)
		}
	})
}

func TestBuildAssetBatch(t *testing.T) {
	t.Parallel()

	t.Run("each file part is named and typed by its content hash", func(t *testing.T) {
		t.Parallel()

		assets := map[string]workerAsset{
			"hash-svg": {Path: "/next.svg", Content: []byte("<svg/>")},
		}
		body, contentType, err := buildAssetBatch([]string{"hash-svg"}, assets)
		if err != nil {
			t.Fatalf("buildAssetBatch: %v", err)
		}
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("parse content type: %v", err)
		}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "hash-svg" || part.FileName() != "hash-svg" {
			t.Errorf("part name/filename = %q/%q, want the content hash for both", part.FormName(), part.FileName())
		}
		if ct := part.Header.Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("part Content-Type = %q, want image/svg+xml", ct)
		}
		data, _ := io.ReadAll(part)
		if string(data) != base64.StdEncoding.EncodeToString([]byte("<svg/>")) {
			t.Errorf("part body must be the base64-encoded contents, got %q", data)
		}
	})

	t.Run("each file part is typed as the AWS asset upload types the same file", func(t *testing.T) {
		t.Parallel()

		want := map[string]string{
			"/_next/static/chunk.js":    "text/javascript; charset=utf-8",
			"/_next/static/css/app.css": "text/css; charset=utf-8",
			"/next.svg":                 "image/svg+xml",
			"/chunk.js.map":             "application/json; charset=utf-8",
			"/favicon.ico":              "image/x-icon",
			"/robots.txt":               "text/plain",
			"/manifest.json":            "application/manifest+json",
			"/index.html":               "text/html; charset=utf-8",
			"/font.woff2":               "font/woff2",
			"/LICENSE":                  "application/octet-stream",
		}
		assets := map[string]workerAsset{}
		var bucket []string
		for p := range want {
			assets[p] = workerAsset{Path: p, Content: []byte("x")}
			bucket = append(bucket, p)
		}
		body, contentType, err := buildAssetBatch(bucket, assets)
		if err != nil {
			t.Fatalf("buildAssetBatch: %v", err)
		}
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("parse content type: %v", err)
		}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		parts := 0
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			parts++
			if got := part.Header.Get("Content-Type"); got != want[part.FormName()] {
				t.Errorf("%s part Content-Type = %q, want %q", part.FormName(), got, want[part.FormName()])
			}
		}
		if parts != len(want) {
			t.Errorf("parts = %d, want %d", parts, len(want))
		}
	})
}

func TestHashAsset(t *testing.T) {
	t.Parallel()

	t.Run("matches the algorithm wrangler uploads under", func(t *testing.T) {
		t.Parallel()

		got := hashAsset(workerAsset{Path: "/greeting.txt", Content: []byte("hello")})
		if want := "129d0bf9c674d4cc340cf5f8feeb9f36"; got != want {
			t.Fatalf("hashAsset = %q, want %q", got, want)
		}
	})

	t.Run("an extensionless path still hashes to 32 hex chars", func(t *testing.T) {
		t.Parallel()

		if len(hashAsset(workerAsset{Path: "/noext", Content: []byte("anything")})) != 32 {
			t.Errorf("hash must be 32 hex chars")
		}
	})
}

func TestPropagationIsTheRecordCacheWindow(t *testing.T) {
	t.Parallel()

	p := Router{p: &cloudflare{}}
	if !p.Facts().CachesRecords {
		t.Fatal("Facts().CachesRecords = false, but the entry worker serves a promotion from a cached record for RECORD_TTL_MS")
	}
	if got := p.Facts().Propagation; got.Typical != recordTTL || got.Published {
		t.Errorf("Facts().Propagation = %+v, want the entry worker's record cache window %v, unpublished", got, recordTTL)
	}
}

func TestTheRouterReachesFunctionsAndContainers(t *testing.T) {
	t.Parallel()

	facts := Router{p: &cloudflare{}}.Facts()
	if !facts.ReachesFunctions || !facts.ReachesContainers {
		t.Errorf("Facts() = %+v, want a router that reaches functions and containers: the entry worker forwards a container's release to the origin its record names", facts)
	}
}

func TestProviderRequiresItsCredentials(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accountID string
		apiToken  string
		call      func(context.Context, edge.Edge) error
	}{
		{
			name: "Bootstrap without an account id is an error",
			call: func(ctx context.Context, p edge.Edge) error {
				_, err := p.Bootstrap(ctx, environment.TierProduction)
				return err
			},
		},
		{
			name:     "VerifyCredentials without an account id is an error",
			apiToken: "tok",
			call: func(ctx context.Context, p edge.Edge) error {
				_, err := p.Hooks().VerifyCredentials(ctx)
				return err
			},
		},
		{
			name:      "VerifyCredentials without an API token is an error",
			accountID: "acct-123",
			call: func(ctx context.Context, p edge.Edge) error {
				_, err := p.Hooks().VerifyCredentials(ctx)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envAccountID, tc.accountID)
			t.Setenv(envAPIToken, tc.apiToken)

			if err := tc.call(t.Context(), New("ocel", fixedOrigin(testOrigin), nil, Options{})); err == nil {
				t.Fatal("expected an error when the environment names no credential")
			}
		})
	}

	t.Run("the edge checks its credentials and its plan", func(t *testing.T) {
		hooks := New("ocel", fixedOrigin(testOrigin), nil, Options{}).Hooks()
		if hooks.VerifyCredentials == nil {
			t.Error("the cloudflare edge checks no credentials, so a preflight cannot say whether its token answers")
		}
		if hooks.CheckCodeEntitlement == nil {
			t.Error("the cloudflare edge checks no entitlement, so a free plan is found out only when the worker upload fails")
		}
	})
}

func TestTeardown(t *testing.T) {
	for _, tc := range []struct {
		name string
		tier environment.Tier
		want []string
	}{
		{
			name: "production takes the workers production bootstrapped",
			tier: environment.TierProduction,
			want: []string{sharedStoreScriptName},
		},
		{
			name: "preview takes the workers preview bootstrapped",
			tier: environment.TierPreview,
			want: []string{previewStoreScriptName},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envAccountID, "acct")

			m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
			p := m.provider(t)
			if err := p.Teardown(t.Context(), tc.tier); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			assertSet(t, "deleted scripts", m.deletedScripts, tc.want)
			assertNoAPITokenOrR2Calls(t, m)
		})
	}

	t.Run("an unknown tier deletes nothing", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")

		m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
		if err := m.provider(t).Teardown(t.Context(), environment.Tier("nonsense")); err == nil {
			t.Fatal("Teardown(unknown tier) err = nil, want an error")
		}
		if len(m.deletedScripts) != 0 {
			t.Errorf("deleted scripts = %v, want none", m.deletedScripts)
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if err := New("ocel", fixedOrigin(testOrigin), nil, Options{}).Teardown(t.Context(), environment.TierProduction); err == nil {
			t.Fatal("Teardown without an account id err = nil, want an error")
		}
	})
}

func TestCloudflareCachesResponses(t *testing.T) {
	t.Parallel()

	if !New("ocel", fixedOrigin(testOrigin), nil, Options{}).Facts().CachesResponses {
		t.Error("Facts().CachesResponses = false, but Workers Cache stores an origin's responses and revalidates them itself")
	}
}

func TestCloudflareInvalidatesByCacheTag(t *testing.T) {
	t.Parallel()

	if !New("ocel", fixedOrigin(testOrigin), nil, Options{}).Facts().InvalidatesByCacheTag {
		t.Error("Facts().InvalidatesByCacheTag = false, but Serve purges what it cached by the tags its origin stamps")
	}
}

func TestTheCloudflareWorkerShieldsItsOrigin(t *testing.T) {
	t.Parallel()

	if !New("ocel", fixedOrigin(testOrigin), nil, Options{}).Facts().ShieldsOrigin {
		t.Error("Facts().ShieldsOrigin = false, but nothing reaches an origin behind the worker without what the worker presents it")
	}
}

func TestAWorkerUploadDeclaresTheDurableObjectsItNames(t *testing.T) {
	t.Run("a durable object export keeps its state in SQLite", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule(), DurableObjects: []string{"PurgeQueue"}}, "")

		exports, _ := meta["exports"].(map[string]any)
		queue, _ := exports["PurgeQueue"].(map[string]any)
		if queue["type"] != "durable-object" || queue["storage"] != "sqlite" || len(exports) != 1 {
			t.Errorf("exports = %v, want only PurgeQueue, a durable object stored in SQLite", meta["exports"])
		}
	})

	t.Run("beside a cached entrypoint it leaves that entrypoint as it was", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule(), CachedEntrypoints: []string{"Serve"}, DurableObjects: []string{"PurgeQueue"}}, "")

		exports, _ := meta["exports"].(map[string]any)
		serve, _ := exports["Serve"].(map[string]any)
		if serve["type"] != "worker" || len(exports) != 2 {
			t.Errorf("exports = %v, want Serve cached and PurgeQueue beside it", meta["exports"])
		}
		cache, _ := meta["cache_options"].(map[string]any)
		if cache["enabled"] != false {
			t.Errorf("cache_options = %v, want the worker itself uncached", meta["cache_options"])
		}
	})

	t.Run("a worker with only a durable object does not turn caching off", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule(), DurableObjects: []string{"PurgeQueue"}}, "")

		if _, ok := meta["cache_options"]; ok {
			t.Errorf("cache_options = %v, want none: nothing here caches", meta["cache_options"])
		}
	})
}

func TestAWorkerUploadCachesOnlyTheEntrypointsItNames(t *testing.T) {
	t.Run("a worker naming a cached entrypoint turns the worker's cache off and that entrypoint's on", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule(), CachedEntrypoints: []string{"Serve"}}, "")

		cache, _ := meta["cache_options"].(map[string]any)
		if cache["enabled"] != false || cache["cross_version_cache"] != true {
			t.Errorf("cache_options = %v, want the worker uncached and its entries kept across versions", meta["cache_options"])
		}
		exports, _ := meta["exports"].(map[string]any)
		serve, _ := exports["Serve"].(map[string]any)
		entrypointCache, _ := serve["cache"].(map[string]any)
		if serve["type"] != "worker" || entrypointCache["enabled"] != true || len(exports) != 1 {
			t.Errorf("exports = %v, want only Serve, a worker with its cache on", meta["exports"])
		}
	})

	t.Run("a worker naming none carries no cache configuration", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule()}, "")

		for _, key := range []string{"cache_options", "exports"} {
			if _, ok := meta[key]; ok {
				t.Errorf("metadata has %s: %v", key, meta[key])
			}
		}
	})

	t.Run("the compatibility date already enables ctx.exports, so no flag asks for it again", func(t *testing.T) {
		meta := metadataFromMultipart(t, workerScript{Main: testModule(), CachedEntrypoints: []string{"Serve"}}, "")

		flags, _ := meta["compatibility_flags"].([]any)
		if slices.Contains(flags, any("enable_ctx_exports")) {
			t.Errorf("compatibility_flags = %v, want no enable_ctx_exports: workerd refuses it from 2025-11-17", flags)
		}
	})
}
