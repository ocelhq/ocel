package deploy

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func nextManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
		},
	}
}

func nodeManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{{Name: "api", Framework: "express"}},
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "api_handler", Framework: "express", App: "api", RouteId: "/"},
		},
	}
}

func nodeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/api/serve.json":  serveDescriptor(t, "express", "a1b2c3d4e5f60718"),
		"apps/api/index.mjs":   "export default {}",
		"apps/api/config.json": `{"runtime":"nodejs22.x","handler":"index.mjs","framework":"express","app":"api"}`,
	})
}

func twoAppManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Functions: []*contractv1.ManifestFunction{
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

func deployedConfig(cfg Config) Config {
	if cfg.Env == "" {
		cfg.Env = providerkit.ProductionEnv
	}
	return cfg
}

func deployedManifest(manifest *contractv1.Manifest) *contractv1.Manifest {
	apps := manifestApps(manifest)
	for _, app := range apps {
		if app.GetDeploymentId() == "" {
			app.DeploymentId = testDeploymentID
		}
	}
	manifest.Apps = apps
	return manifest
}

type appBuilds struct {
	coords map[string]naming.Coordinate
	caches map[string]*isrConfig
	baked  map[string]appBundle
}

func appBuildsFor(t *testing.T, cfg Config, manifest *contractv1.Manifest) appBuilds {
	t.Helper()
	return bakedBuilds(t, cfg, manifest, nil)
}

func bakedBuilds(t *testing.T, cfg Config, manifest *contractv1.Manifest, baked map[string]appBundle) appBuilds {
	t.Helper()
	cfg, manifest = deployedConfig(cfg), deployedManifest(manifest)
	builds := appBuilds{
		coords: map[string]naming.Coordinate{},
		caches: map[string]*isrConfig{},
		baked:  baked,
	}
	if builds.baked == nil {
		builds.baked = map[string]appBundle{}
	}
	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		id, err := NewIdentity(app.GetDeploymentId(), cfg.Env, builds.baked[name].Fingerprint)
		if err != nil {
			t.Fatalf("deployment identity for %s: %v", name, err)
		}
		coord := storageCoordinate(cfg.Env, manifest.GetSlug(), name, releaseOf(id))
		builds.coords[name] = coord
		if app.GetFramework() != frameworkNext {
			continue
		}
		prefix := isrPrefixOf(coord)
		cache := &isrConfig{
			Coord:    coord,
			Bucket:   cfg.AssetBucket,
			Prefix:   prefix,
			Table:    cfg.StateTable,
			TableARN: cfg.StateTableARN,
		}
		if isrEntriesAdopted(cfg.objectStores()) {
			cache.CacheStoreBucket = cfg.CacheStoreBucket
			cache.WriterURL = cfg.ISRWriterEndpoint + "/" + prefix + "/entry"
			cache.WriterSecret = isrWriteSecret(cfg.ISRWriterSeed, prefix)
		}
		builds.caches[name] = cache
	}
	return builds
}

type quietReporter struct{}

func (quietReporter) Say(string) {}

func (quietReporter) Detail(string) {}

func (quietReporter) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func pushSet(ctx context.Context, set *assetSet, err error) error {
	if err != nil || set == nil {
		return err
	}
	return set.push(ctx, quietReporter{})
}

func pushStaticAssetSet(ctx context.Context, cfg Config, app, framework string, coord naming.Coordinate) error {
	set, err := staticAssetSet(cfg, app, framework, coord)
	return pushSet(ctx, set, err)
}

func uploadStaticAssets(ctx context.Context, cfg Config, manifest *contractv1.Manifest, builds appBuilds) error {
	for _, app := range manifestApps(deployedManifest(manifest)) {
		name := app.GetName()
		if err := pushStaticAssetSet(ctx, deployedConfig(cfg), name, app.GetFramework(), builds.coords[name]); err != nil {
			return err
		}
	}
	return nil
}

func uploadPrerenderAssets(ctx context.Context, cfg Config, builds appBuilds) error {
	for _, name := range slices.Sorted(maps.Keys(builds.coords)) {
		set, err := prerenderAssetSet(deployedConfig(cfg), name, builds.caches[name])
		if err := pushSet(ctx, set, err); err != nil {
			return err
		}
	}
	return nil
}

func uploadEdgeBundles(ctx context.Context, cfg Config, manifest *contractv1.Manifest, builds appBuilds) error {
	for _, app := range manifestApps(deployedManifest(manifest)) {
		name := app.GetName()
		set, _, err := edgeBundleSet(deployedConfig(cfg), name, builds.coords[name], builds.baked[name])
		if err := pushSet(ctx, set, err); err != nil {
			return err
		}
	}
	return nil
}

func releaseTokenFor(deploymentID string) string {
	return releaseOf(deployedAs(deploymentID)).String()
}

func storagePrefixFor(env, slug, app, deploymentID string) string {
	return env + "/" + slug + "/" + app + "/" + releaseTokenFor(deploymentID) + "/"
}

func isrPrefixFor(app, deploymentID string) string {
	return storagePrefixFor("prod", "proj", app, deploymentID) + "isr"
}

func isrKeyFor(app, deploymentID, rest string) string {
	return isrPrefixFor(app, deploymentID) + "/" + rest
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

func TestUploadPrerenderAssets(t *testing.T) {
	t.Parallel()

	t.Run("uploads each app under its own prefix", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			isrKeyFor("admin", testDeploymentID, "cache/dash.cache.json"),
			isrKeyFor("admin", testDeploymentID, "cache/users.cache.json"),
			isrKeyFor("web", testDeploymentID, "cache/index.cache.json"),
		}
		if len(got) != len(want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("seeds the adopted cache store", func(t *testing.T) {
		t.Parallel()
		asset := &fakeUploader{exists: map[string]bool{}}
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset,
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if got := entryPuts(asset.puts); len(got) != 0 {
			t.Errorf("asset bucket received entries %v, want none once a store is adopted", got)
		}
		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{
			isrKeyFor("admin", testDeploymentID, "cache/dash.cache.json"),
			isrKeyFor("admin", testDeploymentID, "cache/users.cache.json"),
			isrKeyFor("admin", testDeploymentID, "tag-clock.json"),
			isrKeyFor("web", testDeploymentID, "cache/index.cache.json"),
			isrKeyFor("web", testDeploymentID, "tag-clock.json"),
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
	})

	t.Run("unadopted store stays on the asset bucket", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
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
	})

	t.Run("seeds the genesis tag snapshot", func(t *testing.T) {
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader: &fakeUploader{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreUploader: store,
		}
		cfg = adoptISRWriter(t, cfg)

		before := time.Now().UnixMilli()
		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}
		after := time.Now().UnixMilli()

		for _, key := range []string{isrKeyFor("web", testDeploymentID, "tag-clock.json"), isrKeyFor("admin", testDeploymentID, "tag-clock.json")} {
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
	})

	t.Run("seeds the genesis into both stores", func(t *testing.T) {
		t.Parallel()
		own := &fakeUploader{exists: map[string]bool{}}
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader: own, CacheStoreBucket: "isr", CacheStoreUploader: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		for _, key := range []string{isrKeyFor("web", testDeploymentID, "tag-clock.json"), isrKeyFor("admin", testDeploymentID, "tag-clock.json")} {
			mine, ok := own.putBodies[key]
			if !ok {
				t.Fatalf("no snapshot seeded into the provider's own bucket at %q; puts = %v", key, own.puts)
			}
			if theirs := store.putBodies[key]; theirs != mine {
				t.Errorf("%s differs between the two stores:\n own %s\nedge %s", key, mine, theirs)
			}
		}
	})

	t.Run("keeps an existing snapshot", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{isrKeyFor("web", testDeploymentID, "tag-clock.json"): true}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader: &fakeUploader{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreUploader: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if _, ok := store.putBodies[isrKeyFor("web", testDeploymentID, "tag-clock.json")]; ok {
			t.Error("an existing snapshot was overwritten, want it left as the publisher last wrote it")
		}
		if _, ok := store.putBodies[isrKeyFor("admin", testDeploymentID, "tag-clock.json")]; !ok {
			t.Error("the other app's snapshot was not seeded; one refusal must not stop the rest")
		}
	})

	t.Run("unadopted store seeds one copy", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
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
	})

	t.Run("no Next app", func(t *testing.T) {
		t.Parallel()
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: t.TempDir(), AssetBucket: "assets", Env: "prod", Uploader: f}
		manifest := &contractv1.Manifest{Slug: "proj"}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, manifest)); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}
		if len(f.puts) != 0 {
			t.Errorf("PutObject called %d times, want 0 for a non-Next manifest", len(f.puts))
		}
	})

	t.Run("no prerenders", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":            `{"buildId":"BID","appName":"web"}`,
			"apps/web/functions/index.func/config.json": `{"id":"/"}`,
		})
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}
		if got := entryPuts(f.puts); len(got) != 0 {
			t.Errorf("uploaded %v, want nothing when there are no prerenders", got)
		}
	})

	t.Run("missing bucket", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":  `{"buildId":"BID","appName":"web"}`,
			"apps/web/cache/index.cache.json": `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		})
		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err == nil {
			t.Fatal("uploadPrerenderAssets = nil, want an error for a missing asset bucket")
		}
	})

	t.Run("uploads cache entries", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":      `{"buildId":"BID","appName":"web"}`,
			"apps/web/cache/index.cache.json":     `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
			"apps/web/cache/blog/post.cache.json": `{"lastModified":2,"value":{"kind":"APP_PAGE"}}`,
		})

		f := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Uploader: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			isrKeyFor("web", testDeploymentID, "cache/blog/post.cache.json"),
			isrKeyFor("web", testDeploymentID, "cache/index.cache.json"),
		}
		if len(got) != len(want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("fetch entries stay on the asset bucket", func(t *testing.T) {
		t.Parallel()
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

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		want := isrKeyFor("web", testDeploymentID, "fetch-cache/") + hash + ".cache.json"
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
	})
}

func TestGenesisSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("matches the publishers format", func(t *testing.T) {
		t.Parallel()
		at := time.UnixMilli(1750000000000)
		got, err := json.Marshal(genesisSnapshot(at))
		if err != nil {
			t.Fatalf("marshal genesis snapshot: %v", err)
		}

		want := nextCacheFixture(t, "genesis-tag-snapshot.json")
		if string(got) != strings.TrimSpace(string(want)) {
			t.Errorf("genesis snapshot = %s, want %s", got, strings.TrimSpace(string(want)))
		}
	})
}

func TestTagSnapshotSuffix(t *testing.T) {
	t.Parallel()

	t.Run("matches the edge contract", func(t *testing.T) {
		t.Parallel()
		body := nextCacheFixture(t, "edge-contract.json")
		var contract struct {
			TagSnapshotSuffix string `json:"tagSnapshotSuffix"`
		}
		if err := json.Unmarshal(body, &contract); err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		if tagSnapshotSuffix != contract.TagSnapshotSuffix {
			t.Errorf("tagSnapshotSuffix = %q, want %q", tagSnapshotSuffix, contract.TagSnapshotSuffix)
		}
	})
}
