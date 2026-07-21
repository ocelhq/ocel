package deploy

import (
	"context"
	"sort"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// staticAppTree seeds two Next apps' build output, each with its own build id
// and its own static/ directory.
func staticAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":        `{"buildId":"WEB1"}`,
		"apps/web/static/next.svg":              "<svg/>",
		"apps/web/static/_next/static/chunk.js": "console.log(1)",
		"apps/admin/routing-manifest.json":      `{"buildId":"ADM1"}`,
		"apps/admin/static/favicon.ico":         "ico",
	})
}

// TestAppAssetR2Prefix pins the ADR 0002 key layout: assets/<project>/<app>/
// <build id>, disjoint from the isr cache-entry prefix.
func TestAppAssetR2Prefix(t *testing.T) {
	got := appAssetR2Prefix("proj", "web", "WEB1")
	want := "assets/proj/web/WEB1"
	if got != want {
		t.Errorf("appAssetR2Prefix = %q, want %q", got, want)
	}
}

// TestUploadStaticAssets_UploadsEachAppUnderItsOwnPrefix proves each app's
// static/ output lands under its own assets/<project>/<app>/<build id>
// prefix in the adopted cache store, so a rollback (which swaps the pointer,
// not the objects) can address an older build's assets by that same key.
func TestUploadStaticAssets_UploadsEachAppUnderItsOwnPrefix(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	got := append([]string(nil), store.puts...)
	sort.Strings(got)
	want := []string{
		"assets/proj/admin/ADM1/favicon.ico",
		"assets/proj/web/WEB1/_next/static/chunk.js",
		"assets/proj/web/WEB1/next.svg",
	}
	if len(got) != len(want) {
		t.Fatalf("uploaded keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, b := range store.buckets {
		if b != "isr" {
			t.Errorf("uploaded into bucket %q, want the adopted store %q", b, "isr")
		}
	}
}

// TestUploadStaticAssets_SetsContentType proves each static object is written
// with the content-type its extension implies (so the R2 store is
// self-describing), and an extension the standard library can't resolve is
// left unset for the worker's own fallback to decide.
func TestUploadStaticAssets_SetsContentType(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":        `{"buildId":"WEB1"}`,
		"apps/web/static/next.svg":              "<svg/>",
		"apps/web/static/_next/static/chunk.js": "console.log(1)",
		"apps/web/static/styles.css":            "body{}",
		"apps/web/static/chunk.js.map":          `{"version":3}`,
	})
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	want := map[string]string{
		"assets/proj/web/WEB1/next.svg":              "image/svg+xml",
		"assets/proj/web/WEB1/_next/static/chunk.js": "text/javascript; charset=utf-8",
		"assets/proj/web/WEB1/styles.css":            "text/css; charset=utf-8",
	}
	for key, ct := range want {
		if got := store.contentTypes[key]; got != ct {
			t.Errorf("content-type for %q = %q, want %q", key, got, ct)
		}
	}
	// An unresolvable extension is left unset so the worker fallback decides.
	if got, ok := store.contentTypes["assets/proj/web/WEB1/chunk.js.map"]; ok {
		t.Errorf("content-type for .map = %q, want unset", got)
	}
}

// TestUploadStaticAssets_UnadoptedStoreUploadsNothing proves a substrate whose
// edge offered no cache store uploads no assets at all: there is nowhere for
// the frozen worker to read them back from, so uploading into the provider's
// own asset bucket would only be dead weight.
func TestUploadStaticAssets_UnadoptedStoreUploadsNothing(t *testing.T) {
	asset := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset}

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	if len(asset.puts) != 0 {
		t.Errorf("asset bucket received %v, want nothing with no adopted store", asset.puts)
	}
}

// TestUploadStaticAssets_NoStaticOutputUploadsNothing proves an app with no
// static/ directory (a pure API app, say) is a no-op rather than an error.
func TestUploadStaticAssets_NoStaticOutputUploadsNothing(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	if len(store.puts) != 0 {
		t.Errorf("uploaded %v, want nothing for an app with no static output", store.puts)
	}
}

// TestBuildDeploymentRecord_AssetPrefixIsTheFullR2KeyRoot proves the record
// carries the same prefix uploadStaticAssets published under, so the frozen
// worker needs no project/app identity of its own to read an asset back.
func TestBuildDeploymentRecord_AssetPrefixIsTheFullR2KeyRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root}
	manifest := &deploymentsv1.Manifest{ProjectId: "proj"}
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, "WEB1", nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if want := "assets/proj/web/WEB1"; record.AssetPrefix != want {
		t.Errorf("AssetPrefix = %q, want %q", record.AssetPrefix, want)
	}
}

// TestBuildDeploymentRecord_IsrPrefixIsTheIsrKeyRoot proves the record carries
// the ISR cache's own key root (isrConfig.Prefix) — the <env>/<project>/<app>/
// <build> root the frozen worker joins its cache-entry and tag-snapshot reads
// onto — and not the DynamoDB tag namespace, which addresses nothing in R2.
func TestBuildDeploymentRecord_IsrPrefixIsTheIsrKeyRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root, Env: "prod"}
	manifest := nextManifest()
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, "WEB1", nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if want := "prod/proj/web/WEB1"; record.IsrPrefix != want {
		t.Errorf("IsrPrefix = %q, want %q", record.IsrPrefix, want)
	}
}

// TestBuildDeploymentRecord_NonNextAppHasNoAssetPrefix proves a non-Next
// app's record carries no AssetPrefix: uploadStaticAssets never publishes
// static output for anything but a Next app, so a prefix here would point at
// a location nothing was ever uploaded to.
func TestBuildDeploymentRecord_NonNextAppHasNoAssetPrefix(t *testing.T) {
	cfg := Config{ArtifactRoot: t.TempDir()}
	manifest := &deploymentsv1.Manifest{ProjectId: "proj"}
	app := &deploymentsv1.ManifestApp{Name: "api", Framework: "express"}

	record, err := buildDeploymentRecord(cfg, manifest, app, "API1", nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.AssetPrefix != "" {
		t.Errorf("AssetPrefix = %q, want empty for a non-Next app", record.AssetPrefix)
	}
}
