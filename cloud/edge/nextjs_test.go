package edge

import (
	"os"
	"path/filepath"
	"testing"
)

// stubResolver answers exactly what a test configures, so an assembly's pulls
// are asserted through the worker it produces.
type stubResolver struct {
	urls       map[string]string
	store      CacheStore
	configured bool
	storeErr   error
}

func (s stubResolver) FunctionURL(routeID string) (string, error) {
	url, ok := s.urls[routeID]
	if !ok {
		return "", errNoURL{routeID}
	}
	return url, nil
}

func (s stubResolver) CacheStore() (CacheStore, bool, error) {
	return s.store, s.configured, s.storeErr
}

type errNoURL struct{ route string }

func (e errNoURL) Error() string { return "no function URL for route " + e.route }

func writeNextArtifacts(t *testing.T) WorkerSource {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "routing-manifest.json"), []byte(`{"buildId":"b1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "static", "next.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return WorkerSource{ArtifactRoot: root, BundlePath: bundle}
}

func fullCacheStore() CacheStore {
	return CacheStore{
		Bucket:        "ocel-assets",
		Prefix:        "prod/proj/web/b1",
		Region:        "us-west-2",
		TagTable:      "ocel-state",
		TagTableIndex: "gsi1",
		TagNamespace:  "TAG#prod#proj#web#b1#",
		Credentials:   Credentials{AccessKeyID: "AKIAEDGE", SecretKey: "secret-edge"},
	}
}

func TestAssembleNextCloudflare_FullyConfigured(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/api/documents"}
	r := stubResolver{
		urls:       map[string]string{"/api/documents": "https://fn.lambda-url.aws/"},
		store:      fullCacheStore(),
		configured: true,
	}

	w, err := assembleNextCloudflare(src, r)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	wantVars := map[string]string{
		"FUNCTION_URLS":           `{"/api/documents":"https://fn.lambda-url.aws/"}`,
		"OCEL_EDGE_ACCESS_KEY_ID": "AKIAEDGE",
		"OCEL_AWS_REGION":         "us-west-2",
		"OCEL_ISR_BUCKET":         "ocel-assets",
		"OCEL_ISR_PREFIX":         "prod/proj/web/b1",
		"OCEL_STATE_TABLE":        "ocel-state",
		"OCEL_STATE_TABLE_INDEX":  "gsi1",
		"OCEL_ISR_TAG_NAMESPACE":  "TAG#prod#proj#web#b1#",
	}
	if len(w.Vars) != len(wantVars) {
		t.Errorf("got %d vars, want %d: %v", len(w.Vars), len(wantVars), w.Vars)
	}
	for k, want := range wantVars {
		if got := w.Vars[k]; got != want {
			t.Errorf("Vars[%s] = %q, want %q", k, got, want)
		}
	}
	if len(w.Secrets) != 1 || w.Secrets["OCEL_EDGE_SECRET_KEY"] != "secret-edge" {
		t.Errorf("Secrets = %v, want only OCEL_EDGE_SECRET_KEY", w.Secrets)
	}
	if _, leaked := w.Vars["OCEL_EDGE_SECRET_KEY"]; leaked {
		t.Error("the signing secret must never appear in plain-text Vars")
	}

	if string(w.Main.Content) != "export default {}" || w.Main.Name != "index.js" {
		t.Errorf("Main = %q / %q", w.Main.Name, w.Main.Content)
	}
	if len(w.Modules) != 1 || w.Modules[0].Name != "routing-manifest.json" || w.Modules[0].ContentType != "text/plain" {
		t.Errorf("Modules = %v, want the routing manifest as a text module", w.Modules)
	}
	if w.AssetBinding != "ASSETS" {
		t.Errorf("AssetBinding = %q, want ASSETS", w.AssetBinding)
	}
	if len(w.Assets) != 1 || w.Assets[0].Path != "/next.svg" {
		t.Errorf("Assets = %v, want /next.svg", w.Assets)
	}
}

func TestAssembleNextCloudflare_UnconfiguredCacheOmitsBindings(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/"}
	r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

	w, err := assembleNextCloudflare(src, r)
	if err != nil {
		t.Fatalf("an unconfigured cache must not fail the deploy: %v", err)
	}
	if w.Secrets != nil {
		t.Errorf("Secrets = %v, want none", w.Secrets)
	}
	if len(w.Vars) != 1 || w.Vars["FUNCTION_URLS"] == "" {
		t.Errorf("Vars = %v, want only FUNCTION_URLS", w.Vars)
	}
}

func TestAssembleNextCloudflare_UnresolvableRouteIsAnError(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/orphan"}

	_, err := assembleNextCloudflare(src, stubResolver{urls: map[string]string{}})
	if err == nil {
		t.Fatal("expected an error for an unresolvable route")
	}
}

func TestCollectStaticAssets_ReadsFilesWithHashAndSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "next.svg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icons", "logo.png"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	assets, err := collectStaticAssets(dir)
	if err != nil {
		t.Fatalf("collectStaticAssets: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("got %d assets, want 2", len(assets))
	}

	byPath := map[string]StaticAsset{}
	for _, a := range assets {
		byPath[a.Path] = a
	}
	svg, ok := byPath["/next.svg"]
	if !ok {
		t.Fatalf("missing /next.svg; got %v", byPath)
	}
	if svg.Size != 5 {
		t.Errorf("/next.svg size = %d, want 5", svg.Size)
	}
	if svg.Hash != hashAsset([]byte("hello"), "svg") {
		t.Errorf("/next.svg hash = %q, want hashAsset of its contents+ext", svg.Hash)
	}
	if _, ok := byPath["/icons/logo.png"]; !ok {
		t.Errorf("missing nested /icons/logo.png; got %v", byPath)
	}
}

func TestCollectStaticAssets_MissingDirYieldsNone(t *testing.T) {
	assets, err := collectStaticAssets(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected no error for missing dir, got %v", err)
	}
	if len(assets) != 0 {
		t.Errorf("expected no assets, got %d", len(assets))
	}
}

func TestHashAsset_MatchesWranglerAlgorithm(t *testing.T) {
	// Reference value computed independently:
	//   sha256(base64("hello") + "txt").hex()[:32]
	if got, want := hashAsset([]byte("hello"), "txt"), "129d0bf9c674d4cc340cf5f8feeb9f36"; got != want {
		t.Fatalf("hashAsset = %q, want %q", got, want)
	}
	if len(hashAsset([]byte("anything"), "")) != 32 {
		t.Errorf("hash must be 32 hex chars")
	}
}
