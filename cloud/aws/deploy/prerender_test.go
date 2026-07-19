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

// twoAppManifest is a manifest carrying two Next.js apps, each with its own
// function.
func twoAppManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
			{LogicalName: "admin_index", Framework: "next", App: "admin"},
		},
	}
}

// twoAppTree seeds two Next apps' build output, each with its own build id and
// its own prerendered cache entry.
func twoAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":    `{"buildId":"WEB1"}`,
		"apps/web/cache/index.cache.json":   `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/admin/routing-manifest.json":  `{"buildId":"ADM1"}`,
		"apps/admin/cache/dash.cache.json":  `{"lastModified":2,"value":{"kind":"APP_PAGE"}}`,
		"apps/admin/cache/users.cache.json": `{"lastModified":3,"value":{"kind":"APP_PAGE"}}`,
	})
}

// TestAppCaches_GivesEachAppItsOwnPrefix proves two apps in one deploy address
// two disjoint slices of the account-global asset bucket. A shared prefix would
// let either app's functions read and overwrite the other's cached pages.
func TestAppCaches_GivesEachAppItsOwnPrefix(t *testing.T) {
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", StateTable: "state", Env: "prod"}

	caches, err := appCaches(cfg, twoAppManifest())
	if err != nil {
		t.Fatalf("appCaches: %v", err)
	}
	if len(caches) != 2 {
		t.Fatalf("got %d caches, want one per app", len(caches))
	}
	if want := "prod/proj/web/WEB1"; caches["web"].Prefix != want {
		t.Errorf("web prefix = %q, want %q", caches["web"].Prefix, want)
	}
	if want := "prod/proj/admin/ADM1"; caches["admin"].Prefix != want {
		t.Errorf("admin prefix = %q, want %q", caches["admin"].Prefix, want)
	}
}

// TestAppCaches_OmitsAnAppWithNoPrerenderedContent proves an app whose framework
// keeps no server-side cache is absent, so its role carries no cache grant at
// all.
func TestAppCaches_OmitsAnAppWithNoPrerenderedContent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root, AssetBucket: "assets", StateTable: "state", Env: "prod"}
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
			{LogicalName: "api_index", Framework: "express", App: "api"},
		},
	}

	caches, err := appCaches(cfg, manifest)
	if err != nil {
		t.Fatalf("appCaches: %v", err)
	}
	if _, ok := caches["api"]; ok {
		t.Errorf("an app with no prerendered content must have no cache, got %+v", caches["api"])
	}
	if caches["web"] == nil {
		t.Error("the Next app must still have its own cache")
	}
}

// TestExecutionRoles_OnePerApp proves each app gets exactly one execution role,
// however many functions it owns, and that a single-app project still provisions
// exactly one.
func TestExecutionRoles_OnePerApp(t *testing.T) {
	roles := executionRoles(map[string]*isrConfig{}, []*deploymentsv1.ManifestFunction{
		{LogicalName: "web_index", App: "web"},
		{LogicalName: "web_blog", App: "web"},
		{LogicalName: "admin_index", App: "admin"},
	})
	if len(roles) != 2 {
		t.Fatalf("got %d roles, want one per app", len(roles))
	}
	// Manifest order, so redeploys declare the same roles in the same order.
	if roles[0].App != "web" || roles[1].App != "admin" {
		t.Errorf("roles = %q/%q, want web then admin", roles[0].App, roles[1].App)
	}

	single := executionRoles(map[string]*isrConfig{}, []*deploymentsv1.ManifestFunction{
		{LogicalName: "web_index", App: "web"},
		{LogicalName: "web_blog", App: "web"},
	})
	if len(single) != 1 {
		t.Fatalf("a single-app project got %d roles, want exactly 1", len(single))
	}
}

// TestExecutionRoles_CarryOnlyTheirOwnAppsCache proves a role's cache grant is
// its own app's and no other's, and that an app with no cache gets no grant.
func TestExecutionRoles_CarryOnlyTheirOwnAppsCache(t *testing.T) {
	caches := map[string]*isrConfig{
		"web":   {Prefix: "prod/proj/web/WEB1"},
		"admin": {Prefix: "prod/proj/admin/ADM1"},
	}
	roles := executionRoles(caches, []*deploymentsv1.ManifestFunction{
		{LogicalName: "web_index", App: "web"},
		{LogicalName: "admin_index", App: "admin"},
		{LogicalName: "api_index", App: "api"},
	})
	if len(roles) != 3 {
		t.Fatalf("got %d roles, want 3", len(roles))
	}
	for _, r := range roles {
		switch r.App {
		case "api":
			if r.Cache != nil {
				t.Errorf("api role carries a cache grant %+v, want none", r.Cache)
			}
		default:
			if r.Cache != caches[r.App] {
				t.Errorf("%s role carries %+v, want its own app's cache", r.App, r.Cache)
			}
		}
	}
}

// TestUploadPrerenderAssets_UploadsEachAppUnderItsOwnPrefix proves every app's
// seeded cache entries land under that app's prefix, not one shared with its
// neighbours.
func TestUploadPrerenderAssets_UploadsEachAppUnderItsOwnPrefix(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	got := append([]string(nil), f.puts...)
	sort.Strings(got)
	want := []string{
		"prod/proj/admin/ADM1/cache/dash.cache.json",
		"prod/proj/admin/ADM1/cache/users.cache.json",
		"prod/proj/web/WEB1/cache/index.cache.json",
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

// TestUploadPrerenderAssets_SeedsTheAdoptedCacheStore proves a deploy onto a
// substrate whose edge offered a cache store seeds into that store and not into
// the provider's asset bucket. The handler that reads these entries back reads
// the same store, so seeding the other bucket would leave every prerendered
// route cold with nothing to show for it — and the keys must not move, because
// the edge worker reads exactly them.
func TestUploadPrerenderAssets_SeedsTheAdoptedCacheStore(t *testing.T) {
	asset := &fakeUploader{exists: map[string]bool{}}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset,
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	if len(asset.puts) != 0 {
		t.Errorf("asset bucket received %v, want nothing once a store is adopted", asset.puts)
	}
	got := append([]string(nil), store.puts...)
	sort.Strings(got)
	want := []string{
		"prod/proj/admin/ADM1/cache/dash.cache.json",
		"prod/proj/admin/ADM1/cache/users.cache.json",
		"prod/proj/web/WEB1/cache/index.cache.json",
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

// TestUploadPrerenderAssets_UnadoptedStoreStaysOnTheAssetBucket proves the
// rollback: a substrate whose edge offered no store seeds where it always did.
func TestUploadPrerenderAssets_UnadoptedStoreStaysOnTheAssetBucket(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	if len(f.buckets) != 3 {
		t.Fatalf("uploaded %d objects, want 3", len(f.buckets))
	}
	for _, b := range f.buckets {
		if b != "assets" {
			t.Errorf("uploaded into bucket %q, want the provider's own %q", b, "assets")
		}
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
