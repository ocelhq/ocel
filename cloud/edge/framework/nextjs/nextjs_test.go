package nextjs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// stubResolver answers exactly what a test configures, so an assembly's pulls
// are asserted through the worker it produces.
type stubResolver struct {
	urls     map[string]string
	creds    edge.Credentials
	hasCreds bool
}

func (s stubResolver) FunctionURL(routeID string) (string, error) {
	url, ok := s.urls[routeID]
	if !ok {
		return "", errNoURL{routeID}
	}
	return url, nil
}

func (s stubResolver) EdgeCredentials() (edge.Credentials, bool) {
	return s.creds, s.hasCreds
}

type errNoURL struct{ route string }

func (e errNoURL) Error() string { return "no function URL for route " + e.route }

func writeNextArtifacts(t *testing.T) edge.WorkerSource {
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
	return edge.WorkerSource{ArtifactRoot: root, BundlePath: bundle}
}

func TestAssembleCloudflare_FullyConfigured(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/api/documents"}
	r := stubResolver{
		urls:     map[string]string{"/api/documents": "https://fn.lambda-url.aws/"},
		creds:    edge.Credentials{AccessKeyID: "AKIAEDGE", SecretKey: "secret-edge"},
		hasCreds: true,
	}

	w, err := AssembleCloudflare(src, r)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	// The worker resolves Function URLs and ISR coordinates from its Deployment
	// record; the only binding it reads here is the signing access key.
	wantVars := map[string]string{
		"OCEL_EDGE_ACCESS_KEY_ID": "AKIAEDGE",
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

func TestAssembleCloudflare_NoCredentialsOmitsSigningBindings(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/"}
	r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

	w, err := AssembleCloudflare(src, r)
	if err != nil {
		t.Fatalf("a substrate predating edge credentials must not fail the deploy: %v", err)
	}
	if w.Secrets != nil {
		t.Errorf("Secrets = %v, want none", w.Secrets)
	}
	if len(w.Vars) != 0 {
		t.Errorf("Vars = %v, want none without edge credentials", w.Vars)
	}
}

// The worker always asks for its object store by name; which store lands there,
// and whether one exists at all, is the edge's to decide at upload.
func TestAssembleCloudflare_AsksForItsObjectStoreByName(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/"}
	r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

	w, err := AssembleCloudflare(src, r)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if w.ObjectStore.Binding != objectStoreBinding {
		t.Errorf("ObjectStore.Binding = %q, want %q", w.ObjectStore.Binding, objectStoreBinding)
	}
	if w.ObjectStore.Bucket != "" {
		t.Errorf("ObjectStore.Bucket = %q, want empty: the edge names the bucket it provisioned", w.ObjectStore.Bucket)
	}
}

func TestAssembleCloudflare_UnresolvableRouteIsAnError(t *testing.T) {
	src := writeNextArtifacts(t)
	src.Routes = []string{"/orphan"}

	_, err := AssembleCloudflare(src, stubResolver{urls: map[string]string{}})
	if err == nil {
		t.Fatal("expected an error for an unresolvable route")
	}
}

func TestCollectStaticAssets_ReadsFilesWithPathAndContent(t *testing.T) {
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

	byPath := map[string]edge.StaticAsset{}
	for _, a := range assets {
		byPath[a.Path] = a
	}
	svg, ok := byPath["/next.svg"]
	if !ok {
		t.Fatalf("missing /next.svg; got %v", byPath)
	}
	if string(svg.Content) != "hello" {
		t.Errorf("/next.svg content = %q, want %q", svg.Content, "hello")
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
