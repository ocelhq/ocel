package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func nextManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
		},
	}
}

func twoAppManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
			{LogicalName: "admin_index", Framework: "next", App: "admin"},
		},
	}
}

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

func TestAppCaches_OmitsAnAppWithNoPrerenderedContent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root, AssetBucket: "assets", StateTable: "state", Env: "prod"}
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
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

func entryPuts(puts []string) []string {
	var out []string
	for _, key := range puts {
		if !strings.HasSuffix(key, tagSnapshotSuffix) {
			out = append(out, key)
		}
	}
	return out
}

func TestUploadPrerenderAssets_UploadsEachAppUnderItsOwnPrefix(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	got := entryPuts(f.puts)
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

func TestUploadPrerenderAssets_SeedsTheAdoptedCacheStore(t *testing.T) {
	asset := &fakeUploader{exists: map[string]bool{}}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset,
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
	cfg = adoptISRWriter(t, cfg)

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	if got := entryPuts(asset.puts); len(got) != 0 {
		t.Errorf("asset bucket received entries %v, want none once a store is adopted", got)
	}
	got := append([]string(nil), store.puts...)
	sort.Strings(got)
	want := []string{
		"prod/proj/admin/ADM1/cache/dash.cache.json",
		"prod/proj/admin/ADM1/cache/users.cache.json",
		"prod/proj/admin/ADM1/tag-clock.json",
		"prod/proj/web/WEB1/cache/index.cache.json",
		"prod/proj/web/WEB1/tag-clock.json",
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

func TestUploadPrerenderAssets_UnadoptedStoreStaysOnTheAssetBucket(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	if len(entryPuts(f.puts)) != 3 {
		t.Fatalf("uploaded %v, want the three cache entries", entryPuts(f.puts))
	}
	for _, b := range f.buckets {
		if b != "assets" {
			t.Errorf("uploaded into bucket %q, want the provider's own %q", b, "assets")
		}
	}
}

func TestUploadPrerenderAssets_SeedsTheGenesisTagSnapshot(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
	cfg = adoptISRWriter(t, cfg)

	before := time.Now().UnixMilli()
	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	after := time.Now().UnixMilli()

	for _, key := range []string{"prod/proj/web/WEB1/tag-clock.json", "prod/proj/admin/ADM1/tag-clock.json"} {
		body, ok := store.putBodies[key]
		if !ok {
			t.Fatalf("no snapshot seeded at %q; puts = %v", key, store.puts)
		}
		var snap tagSnapshot
		if err := json.Unmarshal([]byte(body), &snap); err != nil {
			t.Fatalf("parse seeded snapshot %s: %v", key, err)
		}
		if snap.Version != tagSnapshotVersion {
			t.Errorf("%s version = %d, want %d", key, snap.Version, tagSnapshotVersion)
		}
		if snap.DeployedAt < before || snap.DeployedAt > after {
			t.Errorf("%s deployedAt = %d, want the deploy's own clock in [%d,%d]", key, snap.DeployedAt, before, after)
		}
		if snap.GeneratedAt != snap.DeployedAt {
			t.Errorf("%s generatedAt = %d, want the deploy time %d", key, snap.GeneratedAt, snap.DeployedAt)
		}
		if len(snap.Records) != 0 {
			t.Errorf("%s records = %v, want none: no invalidation predates the build", key, snap.Records)
		}
	}
}

func TestUploadPrerenderAssets_SeedsTheGenesisIntoBothStores(t *testing.T) {
	own := &fakeUploader{exists: map[string]bool{}}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader: own, CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
	cfg = adoptISRWriter(t, cfg)

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	for _, key := range []string{"prod/proj/web/WEB1/tag-clock.json", "prod/proj/admin/ADM1/tag-clock.json"} {
		mine, ok := own.putBodies[key]
		if !ok {
			t.Fatalf("no snapshot seeded into the provider's own bucket at %q; puts = %v", key, own.puts)
		}
		if theirs := store.putBodies[key]; theirs != mine {
			t.Errorf("%s differs between the two stores:\n own %s\nedge %s", key, mine, theirs)
		}
	}
}

func TestUploadPrerenderAssets_KeepsAnExistingSnapshot(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{"prod/proj/web/WEB1/tag-clock.json": true}}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
	cfg = adoptISRWriter(t, cfg)

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	if _, ok := store.putBodies["prod/proj/web/WEB1/tag-clock.json"]; ok {
		t.Error("an existing snapshot was overwritten, want it left as the publisher last wrote it")
	}
	if _, ok := store.putBodies["prod/proj/admin/ADM1/tag-clock.json"]; !ok {
		t.Error("the other app's snapshot was not seeded; one refusal must not stop the rest")
	}
}

func TestUploadPrerenderAssets_UnadoptedStoreSeedsOneCopy(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

	if err := uploadPrerenderAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	var seeded int
	for _, key := range f.puts {
		if strings.HasSuffix(key, "tag-clock.json") {
			seeded++
		}
	}
	if seeded != 2 {
		t.Errorf("seeded %d snapshots, want one per app into the one bucket that holds both roles", seeded)
	}
}

func TestGenesisSnapshot_MatchesThePublishersFormat(t *testing.T) {
	at := time.UnixMilli(1750000000000)
	got, err := json.Marshal(genesisSnapshot(at))
	if err != nil {
		t.Fatalf("marshal genesis snapshot: %v", err)
	}

	path := filepath.Join("..", "..", "..", "packages", "next-cache", "fixtures", "genesis-tag-snapshot.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(got) != strings.TrimSpace(string(want)) {
		t.Errorf("genesis snapshot = %s, want %s", got, strings.TrimSpace(string(want)))
	}
}

func TestTagSnapshotSuffix_MatchesTheEdgeContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "packages", "next-cache", "fixtures", "edge-contract.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var contract struct {
		TagSnapshotSuffix string `json:"tagSnapshotSuffix"`
	}
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if tagSnapshotSuffix != contract.TagSnapshotSuffix {
		t.Errorf("tagSnapshotSuffix = %q, want %q", tagSnapshotSuffix, contract.TagSnapshotSuffix)
	}
}

func TestUploadPrerenderAssets_NoNextApp(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: t.TempDir(), AssetBucket: "assets", Env: "prod", Uploader: f}
	manifest := &deploymentsv1.Manifest{Slug: "proj"}

	if err := uploadPrerenderAssets(context.Background(), cfg, manifest); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	if len(f.puts) != 0 {
		t.Errorf("PutObject called %d times, want 0 for a non-Next manifest", len(f.puts))
	}
}

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
	if got := entryPuts(f.puts); len(got) != 0 {
		t.Errorf("uploaded %v, want nothing when there are no prerenders", got)
	}
}

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

	got := entryPuts(f.puts)
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

func TestUploadPrerenderAssets_FetchEntriesStayOnTheAssetBucket(t *testing.T) {
	hash := "a1b2c3"
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":               `{"buildId":"WEB1"}`,
		"apps/web/cache/index.cache.json":              `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/web/fetch-cache/" + hash + ".cache.json": `{"lastModified":2,"value":{"kind":"FETCH"}}`,
	})

	asset := &fakeUploader{exists: map[string]bool{}}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: asset,
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
	cfg = adoptISRWriter(t, cfg)

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	want := "prod/proj/web/WEB1/fetch-cache/" + hash + ".cache.json"
	if got := entryPuts(asset.puts); len(got) != 1 || got[0] != want {
		t.Fatalf("asset bucket got %v, want exactly [%s]", got, want)
	}
	if asset.buckets[0] != "assets" {
		t.Errorf("fetch entry landed in %q, want the provider's own %q", asset.buckets[0], "assets")
	}
	for _, k := range store.puts {
		if strings.Contains(k, "fetch-cache") {
			t.Errorf("fetch entry %q leaked into the adopted store", k)
		}
	}
}
