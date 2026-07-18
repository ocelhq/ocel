package deploy

import (
	"context"
	"sort"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// nextManifest is a manifest carrying a single Next.js function, enough to trip
// the prerender-upload path.
func nextManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
		},
	}
}

// TestUploadPrerenderAssets_NoNextApp proves the path is a no-op for a manifest
// with no Next.js function: nothing is read or uploaded.
func TestUploadPrerenderAssets_NoNextApp(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: t.TempDir(), AssetBucket: "assets", Env: "prod", Uploader: f}
	manifest := &deploymentsv1.Manifest{ProjectId: "proj"}

	if err := uploadPrerenderAssets(context.Background(), cfg, manifest); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	if len(f.puts) != 0 {
		t.Errorf("PutObject called %d times, want 0 for a non-Next manifest", len(f.puts))
	}
}

// TestUploadPrerenderAssets_NoPrerenders proves a Next app that produced no
// prerender assets uploads nothing and does not error.
func TestUploadPrerenderAssets_NoPrerenders(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":            `{"buildId":"BID","appName":"web"}`,
		"apps/web/functions/index.func/config.json": `{"id":"/"}`,
	})
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	if len(f.puts) != 0 {
		t.Errorf("PutObject called %d times, want 0 when there are no prerenders", len(f.puts))
	}
}

// TestUploadPrerenderAssets_MissingBucket proves the path fails loudly when a
// Next app has cache entries to seed but no asset bucket is configured.
func TestUploadPrerenderAssets_MissingBucket(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":  `{"buildId":"BID","appName":"web"}`,
		"apps/web/cache/index.cache.json": `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
	})
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: root, Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err == nil {
		t.Fatal("uploadPrerenderAssets = nil, want an error for a missing asset bucket")
	}
}

// TestUploadPrerenderAssets_UploadsCacheEntries proves the seeded ISR cache
// entries reach the bucket at exactly the key the cache handler reads:
// <prefix>/cache/<key>.cache.json. The handler joins its own key onto
// OCEL_ISR_PREFIX + "/cache/", so a drift here leaves every route re-rendering
// with no error to show for it. Entries live beside functions/ rather than
// under it, so they need their own crawl.
func TestUploadPrerenderAssets_UploadsCacheEntries(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":      `{"buildId":"BID","appName":"web"}`,
		"apps/web/cache/index.cache.json":     `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/web/cache/blog/post.cache.json": `{"lastModified":2,"value":{"kind":"APP_PAGE"}}`,
	})

	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	got := append([]string(nil), f.puts...)
	sort.Strings(got)
	want := []string{
		"prod/proj/web/BID/cache/blog/post.cache.json",
		"prod/proj/web/BID/cache/index.cache.json",
	}
	if len(got) != len(want) {
		t.Fatalf("uploaded keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
