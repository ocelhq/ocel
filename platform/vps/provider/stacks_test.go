package vps_test

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/runtime/baked"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func keptStore(t *testing.T) (*httptest.Server, map[string][]byte) {
	t.Helper()
	var mu sync.Mutex
	kept := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shop/objects" || r.Header.Get("Authorization") != "Bearer box-credential" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		form := multipart.NewReader(r.Body, params["boundary"])
		for part, err := form.NextPart(); err == nil; part, err = form.NextPart() {
			body, _ := io.ReadAll(part)
			mu.Lock()
			kept[part.FormName()] = body
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, kept
}

func plantBuild(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(buildoutput.AppRoot(root, "web"), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func frontedRelease(t *testing.T) provider.StackSpec {
	t.Helper()
	spec := nextAppBuiltFromItsFunction(t)
	spec.App.Routing = nil
	spec.App.OutputRoot = t.TempDir()
	spec.App.EdgeRouteTable = &provider.EdgeRouteTable{
		Location: router.RouteTableLocation{Format: edge.RouteTableNext, Key: spec.App.StorageCoordinate.RouteTableKey("ab")},
		Table:    []byte(`{"rootFunction":"server"}`),
	}
	spec.App.Values.Sensitive = map[string]string{"API_KEY": "k3y"}
	plantBuild(t, spec.App.OutputRoot, map[string]string{
		"static/_next/static/app.js": "chunk",
		naming.ImageConfigFile:       `{"sizes":[640]}`,
		edge.AppBundleFile:           `{"version":1}`,
	})
	return spec
}

func TestAFrontedReleaseKeepsWhatTheWorkerServesItFromAtCloudflare(t *testing.T) {
	t.Parallel()
	srv, kept := keptStore(t)
	spec := frontedRelease(t)
	coordinate := spec.App.StorageCoordinate

	delivered, err := vps.KeepReleaseObjects(context.Background(), srv.URL, "box-credential", spec)
	if err != nil {
		t.Fatalf("KeepReleaseObjects() = %v", err)
	}

	for _, key := range []string{
		coordinate.AssetKey("_next/static/app.js"),
		coordinate.ImageConfigKey(),
		spec.App.EdgeRouteTable.Location.Key,
		coordinate.BundleKey("bundle.json"),
	} {
		if _, found := kept[key]; !found {
			t.Errorf("kept %v, want %s: the worker reads it from the edge's own store", slices.Sorted(mapKeys(kept)), key)
		}
	}
	if delivered.RouteTableKey != spec.App.EdgeRouteTable.Location.Key || delivered.BundleKey != coordinate.BundleKey("bundle.json") {
		t.Errorf("delivered %+v, want the route table and bundle keys the record names", delivered)
	}
	key, err := base64.StdEncoding.DecodeString(delivered.Envelope)
	if err != nil {
		t.Fatalf("Envelope = %q: %v", delivered.Envelope, err)
	}
	values, err := baked.Open(key, kept[coordinate.BundleKey(baked.EdgeFile)])
	if err != nil || values["API_KEY"] != "k3y" {
		t.Errorf("the sealed values open to %v, %v, want the app's sensitive values for its edge code", values, err)
	}
}

func TestADockerfileReleaseTheWorkerFrontsKeepsNothingAtCloudflare(t *testing.T) {
	t.Parallel()
	srv, kept := keptStore(t)
	spec := aStack(t, anApp())
	spec.App.OutputRoot = t.TempDir()

	if _, err := vps.KeepReleaseObjects(context.Background(), srv.URL, "box-credential", spec); err != nil {
		t.Fatalf("KeepReleaseObjects() = %v", err)
	}

	if len(kept) != 0 {
		t.Errorf("kept %v, want nothing: an image built from a Dockerfile serves its own files", kept)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}
