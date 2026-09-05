package cloudflare

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

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

func writeAppArtifacts(t *testing.T) edge.WorkerSource {
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
	return edge.WorkerSource{ArtifactRoot: root, BundlePath: bundle, Entry: "/"}
}

func assembleFor(t *testing.T) func(edge.WorkerSource, edge.Resolver) (edge.Worker, error) {
	t.Helper()
	return New().(edge.Programmable).AssembleApp
}

func TestAssembleApp(t *testing.T) {
	t.Parallel()

	t.Run("a fully configured source yields every module, binding and asset", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Entry = "/api/documents"
		src.Routes = []string{"/api/documents"}
		r := stubResolver{
			urls:     map[string]string{"/api/documents": "https://fn.lambda-url.aws/"},
			creds:    edge.Credentials{AccessKeyID: "AKIAEDGE", SecretKey: "secret-edge"},
			hasCreds: true,
		}

		w, err := assembleFor(t)(src, r)
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}

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
	})

	t.Run("a bootstrap offering no credentials omits the signing bindings", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		w, err := assembleFor(t)(src, r)
		if err != nil {
			t.Fatalf("a bootstrap predating edge credentials must not fail the deploy: %v", err)
		}
		if w.Secrets != nil {
			t.Errorf("Secrets = %v, want none", w.Secrets)
		}
		if len(w.Vars) != 0 {
			t.Errorf("Vars = %v, want none without edge credentials", w.Vars)
		}
	})

	t.Run("the object store is asked for by name and left unbucketed", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		w, err := assembleFor(t)(src, r)
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		if w.ObjectStore.Binding != objectStoreBinding {
			t.Errorf("ObjectStore.Binding = %q, want %q", w.ObjectStore.Binding, objectStoreBinding)
		}
		if w.ObjectStore.Bucket != "" {
			t.Errorf("ObjectStore.Bucket = %q, want empty: the edge names the bucket it provisioned", w.ObjectStore.Bucket)
		}
	})

	t.Run("an unresolvable route is an error", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Routes = []string{"/orphan"}

		if _, err := assembleFor(t)(src, stubResolver{urls: map[string]string{}}); err == nil {
			t.Fatal("expected an error for an unresolvable route")
		}
	})

	t.Run("a node runtime assembles with no routing module", func(t *testing.T) {
		t.Parallel()

		src := writeDescribedApp(t, "node", false)
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		w, err := assembleFor(t)(src, r)
		if err != nil {
			t.Fatalf("a node runtime emits no routing manifest and must still assemble: %v", err)
		}
		if len(w.Modules) != 0 {
			t.Errorf("Modules = %v, want none for a node runtime", w.Modules)
		}
		if string(w.Main.Content) != "export default {}" {
			t.Errorf("Main = %q", w.Main.Content)
		}
	})

	t.Run("the entry resolves through the routeId the build named", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Entry = "bundle-7"
		src.Routes = []string{"bundle-7", "bundle-0"}
		r := stubResolver{urls: map[string]string{
			"bundle-0": "https://first.lambda-url.aws/",
			"bundle-7": "https://entry.lambda-url.aws/",
		}}

		if _, err := assembleFor(t)(src, r); err != nil {
			t.Fatalf("an entry named by the build must not have to match any convention: %v", err)
		}
	})

	t.Run("an entry that resolves to no Function URL is an error", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Entry = "bundle-7"
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		if _, err := assembleFor(t)(src, r); err == nil {
			t.Fatal("an entry no Function URL was realized for cannot be served")
		}
	})

	t.Run("a routed build naming no entry is served by its manifest alone", func(t *testing.T) {
		t.Parallel()

		src := writeAppArtifacts(t)
		src.Entry = ""
		src.Routes = []string{"/api/documents"}
		r := stubResolver{urls: map[string]string{"/api/documents": "https://fn.lambda-url.aws/"}}

		if _, err := assembleFor(t)(src, r); err != nil {
			t.Fatalf("an app no function serves at its root still routes through its manifest: %v", err)
		}
	})

	t.Run("an unrouted build naming no entry is an error", func(t *testing.T) {
		t.Parallel()

		src := writeDescribedApp(t, "node", false)
		src.Entry = ""
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		if _, err := assembleFor(t)(src, r); err == nil {
			t.Fatal("a front has no route to send traffic to when an unrouted build names no entry")
		}
	})

	t.Run("a build that declares edge routing without a routing manifest is an error", func(t *testing.T) {
		t.Parallel()

		src := writeDescribedApp(t, "next", true)
		src.Routes = []string{"/"}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		if _, err := assembleFor(t)(src, r); err == nil {
			t.Fatal("a build declaring edge routing without a routing manifest is corrupt and must not deploy")
		}
	})

	t.Run("a missing serve descriptor is an error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		bundle := filepath.Join(t.TempDir(), "index.js")
		if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
			t.Fatal(err)
		}
		src := edge.WorkerSource{ArtifactRoot: root, BundlePath: bundle, Routes: []string{"/"}}
		r := stubResolver{urls: map[string]string{"/": "https://fn.lambda-url.aws/"}}

		if _, err := assembleFor(t)(src, r); err == nil {
			t.Fatal("an artifact with neither a routing manifest nor a descriptor must not deploy")
		}
	})
}

func writeDescribedApp(t *testing.T, runtime string, edgeRouting bool) edge.WorkerSource {
	t.Helper()
	root := t.TempDir()
	descriptor := fmt.Sprintf(`{"runtime":%q,"buildId":"b1","edgeRouting":%t,"entry":"/"}`, runtime, edgeRouting)
	if err := os.WriteFile(filepath.Join(root, edge.ServeDescriptorFile), []byte(descriptor), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return edge.WorkerSource{ArtifactRoot: root, BundlePath: bundle, Entry: "/"}
}

func TestCollectStaticAssets(t *testing.T) {
	t.Parallel()

	t.Run("every file is read with its rooted path and content", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("a missing directory yields no assets and no error", func(t *testing.T) {
		t.Parallel()

		assets, err := collectStaticAssets(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("expected no error for missing dir, got %v", err)
		}
		if len(assets) != 0 {
			t.Errorf("expected no assets, got %d", len(assets))
		}
	})
}
