package deploy

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// loaderEdge is an edge that can load code, reporting a fixed runtime — the
// only Provider capability the record's edge-workers slot depends on.
type loaderEdge struct {
	compatDate  string
	compatFlags []string
}

func (loaderEdge) Kind() edge.Kind { return edge.KindCloudflare }

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

// edgeAppTree seeds two Next apps' build output, only one of which has edge
// output — the common shape, since most apps carry no middleware at all.
func edgeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":   `{"buildId":"WEB1"}`,
		"apps/web/edge/bundle.json":        `{"version":1,"mainModule":"main.js"}`,
		"apps/admin/routing-manifest.json": `{"buildId":"ADM1"}`,
	})
}

// TestAppEdgeBundleR2Key pins the build-scoped key layout: edge/<project>/
// <app>/<build id>/bundle.json, its own top segment (never under assets/,
// which the worker serves from) and never content-addressed, so pruning one
// build's prefix can only ever remove that build's bundle.
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

// TestUploadEdgeBundles_UploadsEachBundleUnderItsOwnBuildPrefix proves the
// bundle lands in the adopted cache store under the build's own prefix, typed
// as JSON, and that an app with no edge output uploads nothing.
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

// TestUploadEdgeBundles_ReplacesTheObjectAlreadyUnderTheKey proves the bundle
// is written unconditionally rather than skipped-if-exists. The key is
// build-scoped while the loader id is content-addressed, so an app whose
// generateBuildId is constant would otherwise leave the previous build's bytes
// in place and have the loader cache them under the new bundle's id — serving
// the old edge code from a deployment that no longer contains it.
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

// TestUploadEdgeBundles_ARotationTargetsTheSameBuildKey proves a vars-only
// deploy of an unchanged build adds no edge-bundle object: the key is scoped by
// the build id, not the Deployment identity, so the rotation rewrites the one
// object already there with the same bytes. Reuse here is one object, not a
// skipped write — the write stays unconditional for the reason
// TestUploadEdgeBundles_ReplacesTheObjectAlreadyUnderTheKey pins.
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

// TestUploadEdgeBundles_UnadoptedStoreUploadsNothing proves a substrate whose
// edge offered no cache store uploads nothing: the frozen worker would have
// nowhere to load a bundle back from.
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

// TestBuildDeploymentRecord_EdgeWorkersNamesTheBundleAndItsRuntime proves the
// record carries exactly what the frozen worker needs to load this build's edge
// code: the key uploadEdgeBundles published under, and the runtime the edge
// reported it evaluates loaded code under.
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

	// The store and the frozen worker read this record as JSON, so the wire
	// names are the contract — not the Go field names.
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

// TestBuildDeploymentRecord_NoEdgeOutputOmitsEdgeWorkers proves an app with no
// middleware and no edge route carries no edge-workers slot at all — the worker
// reads its absence as "this deployment has no edge code", so a null or an
// empty object would be a different statement.
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

// TestLoaderID_CoversTheRuntimeNotJustTheBundle proves the id the loader keys
// code by changes whenever anything the loader evaluates changes. The runtime
// settings are part of that: were they left out, bumping the compat date would
// reuse an id across genuinely different code and leave warm isolates on the
// old runtime while cold ones took the new one.
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
