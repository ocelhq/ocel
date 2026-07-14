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
			{LogicalName: "index", Framework: "next"},
		},
	}
}

// TestUploadPrerenderAssets_CrawlsAndKeys proves the crawl finds every
// prerender config + fallback under functions/ (recursing into the .segments/
// subtree), skips the heavy .func directories, and keys each object by
// <env>/<project-id>/<app-id>/<build-id>/<relpath>.
func TestUploadPrerenderAssets_CrawlsAndKeys(t *testing.T) {
	root := writeTree(t, map[string]string{
		"routing-manifest.json":                                             `{"buildId":"BID","appName":"web"}`,
		"functions/index.prerender-config.json":                             `{"pathname":"/"}`,
		"functions/index.prerender-fallback.html":                           "<html>root</html>",
		"functions/index.segments/_tree.segment.rsc.prerender-config.json":  `{"seg":true}`,
		"functions/index.segments/_tree.segment.rsc.prerender-fallback.rsc": "RSC-TREE",
		// A decoy config living inside a .func must be skipped: the crawl never
		// descends into deployed Lambda trees.
		"functions/index.func/config.json":                 `{"id":"/"}`,
		"functions/index.func/decoy.prerender-config.json": "SKIP",
	})

	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	got := append([]string(nil), f.puts...)
	sort.Strings(got)
	want := []string{
		"prod/proj/web/BID/index.prerender-config.json",
		"prod/proj/web/BID/index.prerender-fallback.html",
		"prod/proj/web/BID/index.segments/_tree.segment.rsc.prerender-config.json",
		"prod/proj/web/BID/index.segments/_tree.segment.rsc.prerender-fallback.rsc",
	}
	if len(got) != len(want) {
		t.Fatalf("uploaded keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The fallback body is uploaded verbatim.
	if body := f.putBodies["prod/proj/web/BID/index.prerender-fallback.html"]; body != "<html>root</html>" {
		t.Errorf("fallback body = %q, want %q", body, "<html>root</html>")
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
		"routing-manifest.json":            `{"buildId":"BID","appName":"web"}`,
		"functions/index.func/config.json": `{"id":"/"}`,
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
// Next app has prerender assets but no asset bucket is configured.
func TestUploadPrerenderAssets_MissingBucket(t *testing.T) {
	root := writeTree(t, map[string]string{
		"routing-manifest.json":                 `{"buildId":"BID","appName":"web"}`,
		"functions/index.prerender-config.json": `{"pathname":"/"}`,
	})
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: root, Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err == nil {
		t.Fatal("uploadPrerenderAssets = nil, want an error for a missing asset bucket")
	}
}
