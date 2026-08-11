package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func mainModule() edge.WorkerModule {
	return edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")}
}

func metadataFromMultipart(t *testing.T, worker edge.Worker, assetsJWT string) map[string]any {
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
		meta := metadataFromMultipart(t, edge.Worker{Main: mainModule()}, "")
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

		meta := metadataFromMultipart(t, edge.Worker{Main: mainModule()}, "")
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
		worker edge.Worker
		typ    string
		want   []map[string]string
	}{
		{
			name:   "a secret becomes a secret_text binding carrying its value",
			worker: edge.Worker{Main: mainModule(), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"}},
			typ:    "secret_text",
			want:   []map[string]string{{"name": "OCEL_EDGE_SECRET_KEY", "text": "shh"}},
		},
		{
			name:   "a var becomes a plain_text binding",
			worker: edge.Worker{Main: mainModule(), Vars: map[string]string{"OCEL_EDGE_ACCESS_KEY_ID": "AKIA"}},
			typ:    "plain_text",
			want:   []map[string]string{{"name": "OCEL_EDGE_ACCESS_KEY_ID", "text": "AKIA"}},
		},
		{
			name:   "a secret is never also emitted as plain text",
			worker: edge.Worker{Main: mainModule(), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"}},
			typ:    "plain_text",
			want:   nil,
		},
		{
			name:   "an object store with both halves maps to an r2_bucket binding",
			worker: edge.Worker{Main: mainModule(), ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE", Bucket: "ocel-edge-cache"}},
			typ:    "r2_bucket",
			want:   []map[string]string{{"name": "OCEL_CACHE_STORE", "bucket_name": "ocel-edge-cache"}},
		},
		{
			name:   "an object store with neither half emits no bucket binding",
			worker: edge.Worker{Main: mainModule(), ObjectStore: edge.ObjectStore{}},
			typ:    "r2_bucket",
			want:   nil,
		},
		{
			name:   "an object store binding with no bucket emits no bucket binding",
			worker: edge.Worker{Main: mainModule(), ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"}},
			typ:    "r2_bucket",
			want:   nil,
		},
		{
			name:   "a bucket nothing binds emits no bucket binding",
			worker: edge.Worker{Main: mainModule(), ObjectStore: edge.ObjectStore{Bucket: "ocel-edge-cache"}},
			typ:    "r2_bucket",
			want:   nil,
		},
		{
			name:   "a service maps to a service binding",
			worker: edge.Worker{Main: mainModule(), Services: map[string]string{"DEPLOYMENTS": "ocel-proj-store"}},
			typ:    "service",
			want:   []map[string]string{{"name": "DEPLOYMENTS", "service": "ocel-proj-store"}},
		},
		{
			name:   "a loader binding maps to a worker_loader binding",
			worker: edge.Worker{Main: mainModule(), LoaderBinding: "LOADER"},
			typ:    "worker_loader",
			want:   []map[string]string{{"name": "LOADER"}},
		},
		{
			name:   "no loader binding emits no worker_loader binding",
			worker: edge.Worker{Main: mainModule()},
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

	t.Run("a worker holding only vars emits no other binding", func(t *testing.T) {
		meta := metadataFromMultipart(t, edge.Worker{
			Main: mainModule(),
			Vars: map[string]string{"FUNCTION_URLS": "{}"},
		}, "")
		for _, typ := range []string{"r2_bucket", "worker_loader", "service", "secret_text", "assets"} {
			if got := bindingsByType(meta, typ); len(got) != 0 {
				t.Errorf("%s bindings = %v, want none", typ, got)
			}
		}
		if got := len(bindingsByType(meta, "plain_text")); got != 1 {
			t.Errorf("plain_text bindings = %d, want the worker's vars unchanged", got)
		}
	})
}

func TestBuildScriptMultipart(t *testing.T) {
	t.Run("the assets binding and metadata are gated on a completion JWT", func(t *testing.T) {
		worker := edge.Worker{
			AssetBinding: "ASSETS",
			Main:         mainModule(),
			Vars:         map[string]string{"FUNCTION_URLS": "{}"},
			Assets:       []edge.StaticAsset{{Path: "/a.svg", Content: []byte("a")}},
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
		worker := edge.Worker{
			Main:    mainModule(),
			Modules: []edge.WorkerModule{{Name: "routing-manifest.json", ContentType: "text/plain", Content: []byte(`{"buildId":"b"}`)}},
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
		if got := byName["routing-manifest.json"]; got != `{"buildId":"b"}` {
			t.Errorf("routing-manifest.json part = %q", got)
		}
	})
}

func TestBindObjectStore(t *testing.T) {
	t.Run("takes the bucket from the bootstrap values", func(t *testing.T) {
		worker := edge.Worker{ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"}}

		bound := bindObjectStore(worker, map[string]string{valueKeyCacheBucket: "ocel-edge-cache-preview"})
		if bound.ObjectStore.Bucket != "ocel-edge-cache-preview" {
			t.Errorf("ObjectStore.Bucket = %q, want the bootstrapped bucket", bound.ObjectStore.Bucket)
		}
	})

	t.Run("leaves the bucket empty when bootstrap reported none", func(t *testing.T) {
		worker := edge.Worker{ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"}}

		unbootstrapped := bindObjectStore(worker, map[string]string{"unrelated": "x"})
		if unbootstrapped.ObjectStore.Bucket != "" {
			t.Errorf("ObjectStore.Bucket = %q, want empty when bootstrap reported no cache bucket", unbootstrapped.ObjectStore.Bucket)
		}
	})

	t.Run("a bundle carrying no store still gets the binding", func(t *testing.T) {
		composed := bindObjectStore(
			withService(edge.Worker{Main: mainModule()}, "DEPLOYMENTS", "ocel-proj-store"),
			map[string]string{valueKeyCacheBucket: "ocel-edge-cache"},
		)

		buckets := bindingsByType(metadataFromMultipart(t, composed, ""), "r2_bucket")
		if len(buckets) != 1 {
			t.Fatalf("got %d r2_bucket bindings, want 1", len(buckets))
		}
		if buckets[0]["name"] != "OCEL_CACHE_STORE" {
			t.Errorf("r2_bucket binding name = %v, want OCEL_CACHE_STORE", buckets[0]["name"])
		}
		if buckets[0]["bucket_name"] != "ocel-edge-cache" {
			t.Errorf("r2_bucket bucket_name = %v, want ocel-edge-cache", buckets[0]["bucket_name"])
		}
	})

	t.Run("binding a store after a service leaves both bindings standing", func(t *testing.T) {
		worker := edge.Worker{
			Main:        mainModule(),
			ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"},
		}

		composed := bindObjectStore(
			withService(worker, "DEPLOYMENTS", "ocel-proj-store"),
			map[string]string{valueKeyCacheBucket: "ocel-edge-cache"},
		)
		meta := metadataFromMultipart(t, composed, "")

		buckets := bindingsByType(meta, "r2_bucket")
		if len(buckets) != 1 || buckets[0]["bucket_name"] != "ocel-edge-cache" {
			t.Errorf("r2_bucket bindings = %v, want one bound to ocel-edge-cache", buckets)
		}
		services := bindingsByType(meta, "service")
		if len(services) != 1 || services[0]["service"] != "ocel-proj-store" {
			t.Errorf("service bindings = %v, want one bound to ocel-proj-store", services)
		}
	})
}

func TestBindCodeLoader(t *testing.T) {
	t.Run("a bundle carrying no loader still gets the binding beside its store", func(t *testing.T) {
		composed := bindCodeLoader(bindObjectStore(
			edge.Worker{Main: mainModule()},
			map[string]string{valueKeyCacheBucket: "ocel-edge-cache"},
		))
		meta := metadataFromMultipart(t, composed, "")

		loaders := bindingsByType(meta, "worker_loader")
		if len(loaders) != 1 {
			t.Fatalf("got %d worker_loader bindings, want 1", len(loaders))
		}
		if loaders[0]["name"] != codeLoaderBinding {
			t.Errorf("worker_loader binding name = %v, want %q", loaders[0]["name"], codeLoaderBinding)
		}
		if got := len(bindingsByType(meta, "r2_bucket")); got != 1 {
			t.Errorf("r2_bucket bindings = %d, want the object store's binding to survive", got)
		}
	})
}

func TestBuildAssetBatch(t *testing.T) {
	t.Parallel()

	t.Run("each file part is named and typed by its content hash", func(t *testing.T) {
		t.Parallel()

		assets := map[string]edge.StaticAsset{
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
}

func TestHashAsset(t *testing.T) {
	t.Parallel()

	t.Run("matches the algorithm wrangler uploads under", func(t *testing.T) {
		t.Parallel()

		got := hashAsset(edge.StaticAsset{Path: "/greeting.txt", Content: []byte("hello")})
		if want := "129d0bf9c674d4cc340cf5f8feeb9f36"; got != want {
			t.Fatalf("hashAsset = %q, want %q", got, want)
		}
	})

	t.Run("an extensionless path still hashes to 32 hex chars", func(t *testing.T) {
		t.Parallel()

		if len(hashAsset(edge.StaticAsset{Path: "/noext", Content: []byte("anything")})) != 32 {
			t.Errorf("hash must be 32 hex chars")
		}
	})
}

func TestProviderRequiresItsCredentials(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accountID string
		apiToken  string
		call      func(context.Context, edge.Provider) error
	}{
		{
			name: "DeployApp without an account id is an error",
			call: func(ctx context.Context, p edge.Provider) error {
				_, err := p.DeployApp(ctx, edge.AppDeployment{Name: "ocel-proj-prod"})
				return err
			},
		},
		{
			name: "Bootstrap without an account id is an error",
			call: func(ctx context.Context, p edge.Provider) error {
				_, err := p.Bootstrap(ctx, edge.ClassProduction)
				return err
			},
		},
		{
			name:     "VerifyCredentials without an account id is an error",
			apiToken: "tok",
			call: func(ctx context.Context, p edge.Provider) error {
				_, err := p.(edge.CredentialVerifier).VerifyCredentials(ctx)
				return err
			},
		},
		{
			name:      "VerifyCredentials without an API token is an error",
			accountID: "acct-123",
			call: func(ctx context.Context, p edge.Provider) error {
				_, err := p.(edge.CredentialVerifier).VerifyCredentials(ctx)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envAccountID, tc.accountID)
			t.Setenv(envAPIToken, tc.apiToken)

			if err := tc.call(t.Context(), New()); err == nil {
				t.Fatal("expected an error when the environment names no credential")
			}
		})
	}

	t.Run("the provider satisfies the credential verifier contract", func(t *testing.T) {
		if _, ok := New().(edge.CredentialVerifier); !ok {
			t.Fatal("cloudflare provider does not implement edge.CredentialVerifier")
		}
	})
}

func TestCodeRuntime(t *testing.T) {
	t.Parallel()

	t.Run("reports the compat settings the uploaded script carries", func(t *testing.T) {
		t.Parallel()

		loader, ok := New().(edge.CodeLoader)
		if !ok {
			t.Fatalf("cloudflare provider does not implement edge.CodeLoader")
		}
		date, flags := loader.CodeRuntime()
		if date != compatDate {
			t.Errorf("CodeRuntime date = %q, want %q", date, compatDate)
		}
		if len(flags) != len(compatFlags) || (len(flags) > 0 && flags[0] != compatFlags[0]) {
			t.Errorf("CodeRuntime flags = %v, want %v", flags, compatFlags)
		}
	})
}
