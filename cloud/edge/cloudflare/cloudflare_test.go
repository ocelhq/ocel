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

	"github.com/ocelhq/ocel/cloud/edge"
)

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

func TestBuildScriptMultipartEnablesObservability(t *testing.T) {
	worker := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	meta := metadataFromMultipart(t, worker, "")
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
}

func TestBuildScriptMultipartDisablesObservability(t *testing.T) {
	t.Setenv(envObservability, "off")
	worker := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	meta := metadataFromMultipart(t, worker, "")
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
}

func TestScriptBindings_EmitsSecretTextAndPlainText(t *testing.T) {
	worker := edge.Worker{
		Main:    edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Vars:    map[string]string{"OCEL_EDGE_ACCESS_KEY_ID": "AKIA", "OCEL_ISR_BUCKET": "assets"},
		Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"},
	}

	meta := metadataFromMultipart(t, worker, "")
	bindings, ok := meta["bindings"].([]any)
	if !ok {
		t.Fatalf("metadata has no bindings array: %v", meta["bindings"])
	}

	byName := map[string]map[string]any{}
	for _, b := range bindings {
		m := b.(map[string]any)
		byName[m["name"].(string)] = m
	}

	secret, ok := byName["OCEL_EDGE_SECRET_KEY"]
	if !ok {
		t.Fatal("missing the OCEL_EDGE_SECRET_KEY binding")
	}
	if secret["type"] != "secret_text" {
		t.Errorf("secret binding type = %v, want secret_text", secret["type"])
	}
	if secret["text"] != "shh" {
		t.Errorf("secret binding text = %v, want shh", secret["text"])
	}

	if akid := byName["OCEL_EDGE_ACCESS_KEY_ID"]; akid == nil || akid["type"] != "plain_text" {
		t.Errorf("OCEL_EDGE_ACCESS_KEY_ID should be a plain_text binding, got %v", akid)
	}
}

func TestBuildScriptMultipart_AssetsBindingGatedOnCompletionJWT(t *testing.T) {
	worker := edge.Worker{
		AssetBinding: "ASSETS",
		Main:         edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("x")},
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
}

func TestScriptBindings_MapsTheObjectStoreToAnR2Bucket(t *testing.T) {
	worker := edge.Worker{
		Main:        edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE", Bucket: "ocel-edge-cache"},
	}

	buckets := bindingsByType(metadataFromMultipart(t, worker, ""), "r2_bucket")
	if len(buckets) != 1 {
		t.Fatalf("got %d r2_bucket bindings, want 1", len(buckets))
	}
	if buckets[0]["name"] != "OCEL_CACHE_STORE" {
		t.Errorf("r2_bucket binding name = %v, want OCEL_CACHE_STORE", buckets[0]["name"])
	}
	if buckets[0]["bucket_name"] != "ocel-edge-cache" {
		t.Errorf("r2_bucket bucket_name = %v, want ocel-edge-cache", buckets[0]["bucket_name"])
	}
}

func TestScriptBindings_NoObjectStoreEmitsNoBucketBinding(t *testing.T) {
	cases := map[string]edge.ObjectStore{
		"neither half":   {},
		"no bucket":      {Binding: "OCEL_CACHE_STORE"},
		"unbound bucket": {Bucket: "ocel-edge-cache"},
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			worker := edge.Worker{
				Main:        edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
				Vars:        map[string]string{"FUNCTION_URLS": "{}"},
				ObjectStore: store,
			}
			meta := metadataFromMultipart(t, worker, "")
			if got := bindingsByType(meta, "r2_bucket"); len(got) != 0 {
				t.Errorf("r2_bucket bindings = %v, want none", got)
			}
			if got := len(bindingsByType(meta, "plain_text")); got != 1 {
				t.Errorf("plain_text bindings = %d, want the worker's vars unchanged", got)
			}
		})
	}
}

func TestScriptBindings_MapsServicesToServiceBindings(t *testing.T) {
	worker := edge.Worker{
		Main:     edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Services: map[string]string{"DEPLOYMENTS": "ocel-proj-store"},
	}

	services := bindingsByType(metadataFromMultipart(t, worker, ""), "service")
	if len(services) != 1 {
		t.Fatalf("got %d service bindings, want 1", len(services))
	}
	if services[0]["name"] != "DEPLOYMENTS" {
		t.Errorf("service binding name = %v, want DEPLOYMENTS", services[0]["name"])
	}
	if services[0]["service"] != "ocel-proj-store" {
		t.Errorf("service binding service = %v, want ocel-proj-store", services[0]["service"])
	}
}

func TestBindObjectStore_FrozenBundleStillGetsTheBinding(t *testing.T) {
	frozen := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	composed := bindObjectStore(withService(frozen, "DEPLOYMENTS", "ocel-proj-store"), map[string]string{valueKeyCacheBucket: "ocel-edge-cache"})
	meta := metadataFromMultipart(t, composed, "")

	buckets := bindingsByType(meta, "r2_bucket")
	if len(buckets) != 1 {
		t.Fatalf("got %d r2_bucket bindings, want 1", len(buckets))
	}
	if buckets[0]["name"] != "OCEL_CACHE_STORE" {
		t.Errorf("r2_bucket binding name = %v, want OCEL_CACHE_STORE", buckets[0]["name"])
	}
	if buckets[0]["bucket_name"] != "ocel-edge-cache" {
		t.Errorf("r2_bucket bucket_name = %v, want ocel-edge-cache", buckets[0]["bucket_name"])
	}
}

func TestBindObjectStore_TakesTheBucketFromBootstrapValues(t *testing.T) {
	worker := edge.Worker{ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"}}

	bound := bindObjectStore(worker, map[string]string{valueKeyCacheBucket: "ocel-edge-cache-preview"})
	if bound.ObjectStore.Bucket != "ocel-edge-cache-preview" {
		t.Errorf("ObjectStore.Bucket = %q, want the bootstrapped bucket", bound.ObjectStore.Bucket)
	}

	unbootstrapped := bindObjectStore(worker, map[string]string{"unrelated": "x"})
	if unbootstrapped.ObjectStore.Bucket != "" {
		t.Errorf("ObjectStore.Bucket = %q, want empty when bootstrap reported no cache bucket", unbootstrapped.ObjectStore.Bucket)
	}
}

func TestBindObjectStoreThenWithService_BothBindingsSurvive(t *testing.T) {
	worker := edge.Worker{
		Main:        edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		ObjectStore: edge.ObjectStore{Binding: "OCEL_CACHE_STORE"},
	}

	composed := bindObjectStore(withService(worker, "DEPLOYMENTS", "ocel-proj-store"), map[string]string{valueKeyCacheBucket: "ocel-edge-cache"})
	meta := metadataFromMultipart(t, composed, "")

	buckets := bindingsByType(meta, "r2_bucket")
	if len(buckets) != 1 || buckets[0]["bucket_name"] != "ocel-edge-cache" {
		t.Errorf("r2_bucket bindings = %v, want one bound to ocel-edge-cache", buckets)
	}
	services := bindingsByType(meta, "service")
	if len(services) != 1 || services[0]["service"] != "ocel-proj-store" {
		t.Errorf("service bindings = %v, want one bound to ocel-proj-store", services)
	}
}

func TestBuildScriptMultipart_UploadsMainAndSiblingModules(t *testing.T) {
	worker := edge.Worker{
		Main:    edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
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
}

func TestBuildAssetBatch_EncodesFilePartsPerHash(t *testing.T) {
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
}

func TestCoveredByUniversalSSL(t *testing.T) {
	cases := []struct {
		host, zone string
		want       bool
	}{
		{"acme.com", "acme.com", true},
		{"www.acme.com", "acme.com", true},
		{"*.acme.com", "acme.com", true},
		{"*.preview.acme.com", "acme.com", false},
		{"a.b.acme.com", "acme.com", false},
		{"other.com", "acme.com", false},
	}
	for _, tc := range cases {
		if got := coveredByUniversalSSL(tc.host, tc.zone); got != tc.want {
			t.Errorf("coveredByUniversalSSL(%q, %q) = %v, want %v", tc.host, tc.zone, got, tc.want)
		}
	}
}

func TestCanonicalDomainURL(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		want    string
	}{
		{"none", nil, ""},
		{"single plain", []string{"app.com"}, "https://app.com"},
		{"first non-wildcard wins", []string{"*.app.com", "app.com"}, "https://app.com"},
		{"all wildcard falls back to first", []string{"*.app.com"}, "https://*.app.com"},
	}
	for _, tc := range cases {
		if got := canonicalDomainURL(tc.domains); got != tc.want {
			t.Errorf("%s: canonicalDomainURL(%v) = %q, want %q", tc.name, tc.domains, got, tc.want)
		}
	}
}

func TestRouteBaseDomain(t *testing.T) {
	if got := routeBaseDomain("*.preview.acme.com"); got != "preview.acme.com" {
		t.Errorf("routeBaseDomain wildcard = %q, want preview.acme.com", got)
	}
	if got := routeBaseDomain("www.acme.com"); got != "www.acme.com" {
		t.Errorf("routeBaseDomain plain = %q, want www.acme.com", got)
	}
}

func TestZoneOwns(t *testing.T) {
	cases := []struct {
		hostname string
		zone     string
		want     bool
	}{
		{"app.acme.com", "acme.com", true},
		{"acme.com", "acme.com", true},
		{"app.acme.com", "app.acme.com", true},
		{"app.acme.com", "other.com", false},
		{"app.acme.com", "me.com", false},
		{"notacme.com", "acme.com", false},
		{"app.acme.com", "cme.com", false},
	}
	for _, tc := range cases {
		if got := zoneOwns(tc.hostname, tc.zone); got != tc.want {
			t.Errorf("zoneOwns(%q, %q) = %v, want %v", tc.hostname, tc.zone, got, tc.want)
		}
	}
}

func TestDeployApp_MissingAccountID_Errors(t *testing.T) {
	t.Setenv(envAccountID, "")

	_, err := New().DeployApp(context.Background(), edge.AppDeployment{Name: "ocel-proj-prod"})
	if err == nil {
		t.Fatalf("expected an error when %s is unset", envAccountID)
	}
}

func TestBootstrap_MissingAccountID_Errors(t *testing.T) {
	t.Setenv(envAccountID, "")

	_, err := New().Bootstrap(context.Background(), edge.ClassProduction)
	if err == nil {
		t.Fatalf("expected an error when %s is unset", envAccountID)
	}
}

func TestVerifyCredentials_MissingAccountID_Errors(t *testing.T) {
	t.Setenv(envAccountID, "")
	t.Setenv(envAPIToken, "tok")

	v, ok := New().(edge.CredentialVerifier)
	if !ok {
		t.Fatalf("cloudflare provider does not implement edge.CredentialVerifier")
	}
	if _, err := v.VerifyCredentials(context.Background()); err == nil {
		t.Fatalf("expected an error when %s is unset", envAccountID)
	}
}

func TestVerifyCredentials_MissingAPIToken_Errors(t *testing.T) {
	t.Setenv(envAccountID, "acct-123")
	t.Setenv(envAPIToken, "")

	v := New().(edge.CredentialVerifier)
	if _, err := v.VerifyCredentials(context.Background()); err == nil {
		t.Fatalf("expected an error when %s is unset", envAPIToken)
	}
}

func TestHashAsset_MatchesWranglerAlgorithm(t *testing.T) {
	got := hashAsset(edge.StaticAsset{Path: "/greeting.txt", Content: []byte("hello")})
	if want := "129d0bf9c674d4cc340cf5f8feeb9f36"; got != want {
		t.Fatalf("hashAsset = %q, want %q", got, want)
	}
	if len(hashAsset(edge.StaticAsset{Path: "/noext", Content: []byte("anything")})) != 32 {
		t.Errorf("hash must be 32 hex chars")
	}
}

func TestScriptBindings_MapsTheCodeLoaderToAWorkerLoader(t *testing.T) {
	worker := edge.Worker{
		Main:          edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		LoaderBinding: "LOADER",
	}

	loaders := bindingsByType(metadataFromMultipart(t, worker, ""), "worker_loader")
	if len(loaders) != 1 {
		t.Fatalf("got %d worker_loader bindings, want 1", len(loaders))
	}
	if loaders[0]["name"] != "LOADER" {
		t.Errorf("worker_loader binding name = %v, want LOADER", loaders[0]["name"])
	}
}

func TestScriptBindings_NoCodeLoaderEmitsNoLoaderBinding(t *testing.T) {
	worker := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Vars: map[string]string{"FUNCTION_URLS": "{}"},
	}

	meta := metadataFromMultipart(t, worker, "")
	if got := bindingsByType(meta, "worker_loader"); len(got) != 0 {
		t.Errorf("worker_loader bindings = %v, want none", got)
	}
	if got := len(bindingsByType(meta, "plain_text")); got != 1 {
		t.Errorf("plain_text bindings = %d, want the worker's vars unchanged", got)
	}
}

func TestBindCodeLoader_FrozenBundleStillGetsTheBinding(t *testing.T) {
	frozen := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	composed := bindCodeLoader(bindObjectStore(frozen, map[string]string{valueKeyCacheBucket: "ocel-edge-cache"}))
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
}

func TestCodeRuntime_ReportsTheUploadedScriptsCompatSettings(t *testing.T) {
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
}
