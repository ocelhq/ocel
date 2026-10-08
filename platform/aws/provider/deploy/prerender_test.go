package deploy

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func nextManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{{Name: "web", Framework: &contractv1.Framework{Name: "next"},
			Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "web_index", Framework: &contractv1.Framework{Name: "next"}})}},
	}
}

func serverlessArtifact(functions ...*contractv1.ManifestFunction) *contractv1.ManifestApp_Serverless {
	return &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: functions}}
}

func nodeManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{{Name: "api", Framework: &contractv1.Framework{Name: "node"},
			Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "api_handler", Framework: &contractv1.Framework{Name: "node"}, RouteId: "/"})}},
	}
}

func nodeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/api/hosting.json":         hostingJSON(t, "express", "a1b2c3d4e5f60718"),
		"apps/api/index.mjs":            "export default {}",
		"apps/api/function-config.json": `{"framework":{"name":"node"},"entryFile":"index.mjs","app":"api"}`,
	})
}

func twoAppManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: &contractv1.Framework{Name: "next"},
				Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "web_index", Framework: &contractv1.Framework{Name: "next"}})},
			{Name: "admin", Framework: &contractv1.Framework{Name: "next"},
				Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "admin_index", Framework: &contractv1.Framework{Name: "next"}})},
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
		cfg.Env = stackrecords.ProductionEnv
	}
	return cfg
}

func deployedManifest(manifest *contractv1.Manifest) *contractv1.Manifest {
	for _, app := range manifest.GetApps() {
		if app.GetBuildId() == "" {
			app.BuildId = testBuildID
		}
	}
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
	for _, app := range manifest.GetApps() {
		name := app.GetName()
		id, err := provider.NewRelease(app.GetBuildId(), "p1", cfg.Env, builds.baked[name].Fingerprint)
		if err != nil {
			t.Fatalf("release for %s: %v", name, err)
		}
		coord := storageCoordinate(cfg.Env, manifest.GetSlug(), name, id.Token())
		builds.coords[name] = coord
		if app.GetFramework().GetName() != buildoutput.FrameworkNext {
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
			cache.WriterURL = cfg.ISRWriterEndpoint
			cache.WriterSecret = cloudflare.DeriveISRWriteSecret(cfg.ISRWriterSeed, prefix)
		}
		builds.caches[name] = cache
	}
	return builds
}

type quietProgress struct{}

func (quietProgress) Say(string) {}

func (quietProgress) Warn(string) {}

func (quietProgress) Error(string) {}

func (quietProgress) Detail(string) {}

func (quietProgress) Debug(string) {}

func (quietProgress) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func pushSet(ctx context.Context, set *assetSet, err error) error {
	if err != nil || set == nil {
		return err
	}
	return set.push(ctx, quietProgress{})
}

func pushStaticAssetSet(ctx context.Context, cfg Config, app, runtime string, coord naming.Coordinate) error {
	set, err := staticAssetSet(cfg, app, runtime, coord)
	return pushSet(ctx, set, err)
}

func uploadStaticAssets(ctx context.Context, cfg Config, manifest *contractv1.Manifest, builds appBuilds) error {
	for _, app := range deployedManifest(manifest).GetApps() {
		name := app.GetName()
		if err := pushStaticAssetSet(ctx, deployedConfig(cfg), name, app.GetFramework().GetName(), builds.coords[name]); err != nil {
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
	for _, app := range deployedManifest(manifest).GetApps() {
		name := app.GetName()
		set, _, err := edgeBundleSet(deployedConfig(cfg), name, builds.coords[name], builds.baked[name])
		if err := pushSet(ctx, set, err); err != nil {
			return err
		}
	}
	return nil
}

func releaseTokenFor(buildID string) string {
	return deployedAs(buildID).Token().String()
}

func storagePrefixFor(env, slug, app, buildID string) string {
	return env + "/" + slug + "/" + app + "/" + releaseTokenFor(buildID) + "/"
}

func isrPrefixFor(app, buildID string) string {
	return storagePrefixFor("prod", "proj", app, buildID) + "isr"
}

func isrKeyFor(app, buildID, rest string) string {
	return isrPrefixFor(app, buildID) + "/" + rest
}

func entryPuts(puts []string) []string {
	var out []string
	for _, key := range puts {
		if !strings.HasSuffix(key, "/tag-clock.json") {
			out = append(out, key)
		}
	}
	return out
}

func TestUploadPrerenderAssets(t *testing.T) {
	t.Parallel()

	t.Run("uploads each app under its own prefix", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Objects: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			isrKeyFor("admin", testBuildID, "cache/dash.cache.json"),
			isrKeyFor("admin", testBuildID, "cache/users.cache.json"),
			isrKeyFor("web", testBuildID, "cache/index.cache.json"),
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
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Objects: asset,
			CacheStoreBucket: "isr", CacheStoreObjects: store,
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
			isrKeyFor("admin", testBuildID, "cache/dash.cache.json"),
			isrKeyFor("admin", testBuildID, "cache/users.cache.json"),
			isrKeyFor("admin", testBuildID, "tag-clock.json"),
			isrKeyFor("web", testBuildID, "cache/index.cache.json"),
			isrKeyFor("web", testBuildID, "tag-clock.json"),
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
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Objects: f}

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
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Objects: &fakeArtifactStore{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreObjects: store,
		}
		cfg = adoptISRWriter(t, cfg)

		before := time.Now().UnixMilli()
		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}
		after := time.Now().UnixMilli()

		for _, key := range []string{isrKeyFor("web", testBuildID, "tag-clock.json"), isrKeyFor("admin", testBuildID, "tag-clock.json")} {
			body, ok := store.putBodies[key]
			if !ok {
				t.Fatalf("no snapshot seeded at %q; puts = %v", key, store.puts)
			}
			var snap prerender.TagSnapshot
			if err := json.Unmarshal([]byte(body), &snap); err != nil {
				t.Fatalf("parse seeded snapshot %s: %v", key, err)
			}
			if snap.Version != prerender.TagSnapshotVersion {
				t.Errorf("%s version = %d, want %d", key, snap.Version, prerender.TagSnapshotVersion)
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
		own := &fakeArtifactStore{exists: map[string]bool{}}
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Objects: own, CacheStoreBucket: "isr", CacheStoreObjects: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		for _, key := range []string{isrKeyFor("web", testBuildID, "tag-clock.json"), isrKeyFor("admin", testBuildID, "tag-clock.json")} {
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
		store := &fakeArtifactStore{exists: map[string]bool{isrKeyFor("web", testBuildID, "tag-clock.json"): true}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Objects: &fakeArtifactStore{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreObjects: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if _, ok := store.putBodies[isrKeyFor("web", testBuildID, "tag-clock.json")]; ok {
			t.Error("an existing snapshot was overwritten, want it left as the publisher last wrote it")
		}
		if _, ok := store.putBodies[isrKeyFor("admin", testBuildID, "tag-clock.json")]; !ok {
			t.Error("the other app's snapshot was not seeded; one refusal must not stop the rest")
		}
	})

	t.Run("unadopted store seeds one copy", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod", Objects: f}

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
			t.Errorf("seeded %d snapshots, want one per app into the one bucket that serves both roles", seeded)
		}
	})

	t.Run("no Next app", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: t.TempDir(), AssetBucket: "assets", Env: "prod", Objects: f}
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
			"apps/web/routing-manifest.json":                     `{"buildId":"BID","appName":"web"}`,
			"apps/web/functions/index.func/function-config.json": `{"id":"/"}`,
		})
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Objects: f}

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
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, Env: "prod", Objects: f}

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

		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Objects: f}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			isrKeyFor("web", testBuildID, "cache/blog/post.cache.json"),
			isrKeyFor("web", testBuildID, "cache/index.cache.json"),
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

		asset := &fakeArtifactStore{exists: map[string]bool{}}
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: root, AssetBucket: "assets", Env: "prod", Objects: asset,
			CacheStoreBucket: "isr", CacheStoreObjects: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		want := isrKeyFor("web", testBuildID, "fetch-cache/") + hash + ".cache.json"
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
