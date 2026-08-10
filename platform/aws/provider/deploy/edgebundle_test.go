package deploy

import (
	"context"
	"encoding/json"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"sort"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type loaderEdge struct {
	compatDate  string
	compatFlags []string
}

func (loaderEdge) Kind() edge.Kind { return edge.KindCloudflare }

func (loaderEdge) AssembleApp(src edge.WorkerSource, r edge.Resolver) (edge.Worker, error) {
	return cloudflare.New().AssembleApp(src, r)
}

func (loaderEdge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{}, nil
}

func (loaderEdge) DeployApp(context.Context, edge.AppDeployment) (edge.AppResult, error) {
	return edge.AppResult{}, nil
}

func (e loaderEdge) CodeRuntime() (string, []string) { return e.compatDate, e.compatFlags }

func testLoaderEdge() loaderEdge {
	return loaderEdge{compatDate: "2026-07-13", compatFlags: []string{"nodejs_compat"}}
}

func edgeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":   `{"buildId":"WEB1"}`,
		"apps/web/edge/bundle.json":        `{"version":1,"mainModule":"main.js"}`,
		"apps/admin/routing-manifest.json": `{"buildId":"ADM1"}`,
	})
}

func TestAppEdgeBundleR2Key(t *testing.T) {
	got := appEdgeBundleR2Key("proj", "web", "WEB1")
	want := "edge/proj/web/WEB1/bundle.json"
	if got != want {
		t.Errorf("appEdgeBundleR2Key = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, appEdgeR2Prefix("proj", "web", "WEB1")+"/") {
		t.Errorf("key %q must live under the build's own prune-able prefix", got)
	}
	if other := appEdgeBundleR2Key("proj", "web", "WEB2"); other == got {
		t.Error("two builds of one app must not share a bundle key")
	}
}

func TestUploadEdgeBundles_UploadsEachBundleUnderItsOwnBuildPrefix(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}

	got := append([]string(nil), store.puts...)
	sort.Strings(got)
	want := []string{"edge/proj/web/WEB1/bundle.json"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("uploaded keys = %v, want %v", got, want)
	}
	if ct := store.contentTypes[want[0]]; ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if body := store.putBodies[want[0]]; body != `{"version":1,"mainModule":"main.js"}` {
		t.Errorf("uploaded body = %q, want the bundle verbatim", body)
	}
	for _, b := range store.buckets {
		if b != "isr" {
			t.Errorf("uploaded into bucket %q, want the adopted store %q", b, "isr")
		}
	}
}

func TestUploadEdgeBundles_ReplacesTheObjectAlreadyUnderTheKey(t *testing.T) {
	key := "edge/proj/web/WEB1/bundle.json"
	store := &fakeUploader{exists: map[string]bool{key: true}}
	cfg := Config{
		ArtifactRoot: writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
			"apps/web/edge/bundle.json":      `{"version":1,"mainModule":"main.js","shim":"REBUILT"}`,
		}),
		AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}

	if len(store.puts) != 1 || store.puts[0] != key {
		t.Fatalf("uploaded keys = %v, want the bundle rewritten at %q", store.puts, key)
	}
	if !strings.Contains(store.putBodies[key], "REBUILT") {
		t.Errorf("object at %q = %q, want this build's bundle", key, store.putBodies[key])
	}
}

func TestUploadEdgeBundles_ARotationTargetsTheSameBuildKey(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("first uploadEdgeBundles: %v", err)
	}
	for _, key := range store.puts {
		store.exists[key] = true
	}
	if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("rotation uploadEdgeBundles: %v", err)
	}

	distinct := map[string]bool{}
	for _, key := range store.puts {
		distinct[key] = true
	}
	if len(store.puts) != 2 || len(distinct) != 1 || !distinct["edge/proj/web/WEB1/bundle.json"] {
		t.Fatalf("uploaded keys = %v, want the same %q twice", store.puts, "edge/proj/web/WEB1/bundle.json")
	}
	if body := store.putBodies["edge/proj/web/WEB1/bundle.json"]; body != `{"version":1,"mainModule":"main.js"}` {
		t.Errorf("object after the rotation = %q, want the build's bundle unchanged", body)
	}
}

func TestUploadEdgeBundles_UnadoptedStoreUploadsNothing(t *testing.T) {
	asset := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset}

	if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}
	if len(asset.puts) != 0 {
		t.Errorf("asset bucket received %v, want nothing with no adopted store", asset.puts)
	}
}

func TestBuildDeploymentRecord_EdgeWorkersNamesTheBundleAndItsRuntime(t *testing.T) {
	cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
	manifest := nextManifest()
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.EdgeWorkers == nil {
		t.Fatal("EdgeWorkers = nil, want the build's edge bundle")
	}
	if want := "edge/proj/web/WEB1/bundle.json"; record.EdgeWorkers.BundleKey != want {
		t.Errorf("BundleKey = %q, want %q", record.EdgeWorkers.BundleKey, want)
	}
	if record.EdgeWorkers.CompatDate != "2026-07-13" {
		t.Errorf("CompatDate = %q, want the edge's own", record.EdgeWorkers.CompatDate)
	}
	if len(record.EdgeWorkers.CompatFlags) != 1 || record.EdgeWorkers.CompatFlags[0] != "nodejs_compat" {
		t.Errorf("CompatFlags = %v, want the edge's own", record.EdgeWorkers.CompatFlags)
	}
	if len(record.EdgeWorkers.ID) != 64 {
		t.Errorf("ID = %q, want a sha256 hex digest", record.EdgeWorkers.ID)
	}

	raw, err := json.Marshal(record.EdgeWorkers)
	if err != nil {
		t.Fatalf("marshal edge workers: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal edge workers: %v", err)
	}
	for _, key := range []string{"bundleKey", "id", "compatDate", "compatFlags"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("edge workers JSON %s is missing %q", raw, key)
		}
	}
	if len(wire) != 4 {
		t.Errorf("edge workers JSON = %s, want exactly the four contracted keys", raw)
	}
}

func TestBuildDeploymentRecord_NoEdgeOutputOmitsEdgeWorkers(t *testing.T) {
	cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
	manifest := twoAppManifest()
	app := &deploymentsv1.ManifestApp{Name: "admin", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("ADM1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.EdgeWorkers != nil {
		t.Errorf("EdgeWorkers = %+v, want none for a build with no edge output", record.EdgeWorkers)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if strings.Contains(string(raw), "edgeWorkers") {
		t.Errorf("record JSON = %s, want no edgeWorkers key at all", raw)
	}
}

func TestLoaderID_CoversTheRuntimeNotJustTheBundle(t *testing.T) {
	bundle := []byte(`{"version":1}`)
	base := loaderID(bundle, "2026-07-13", []string{"nodejs_compat"})

	if again := loaderID(bundle, "2026-07-13", []string{"nodejs_compat"}); again != base {
		t.Error("id changed with nothing changed; a redeploy must reuse the loaded code")
	}
	for name, got := range map[string]string{
		"changed bundle":      loaderID([]byte(`{"version":2}`), "2026-07-13", []string{"nodejs_compat"}),
		"changed compat date": loaderID(bundle, "2026-09-01", []string{"nodejs_compat"}),
		"changed compat flag": loaderID(bundle, "2026-07-13", []string{"nodejs_compat", "no_nodejs_compat_v2"}),
		"dropped compat flag": loaderID(bundle, "2026-07-13", nil),
	} {
		if got == base {
			t.Errorf("%s: id unchanged, want a new id for code the loader evaluates differently", name)
		}
	}
}
