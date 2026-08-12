package deploy

import (
	"context"
	"encoding/json"
	"slices"
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

func nodeManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "api", Framework: "express"}},
		Functions: []*deploymentsv1.ManifestFunction{
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

func appBuildsFor(t *testing.T, cfg Config, manifest *deploymentsv1.Manifest) appBuilds {
	t.Helper()
	builds, err := resolveAppBuilds(cfg, manifest, nil)
	if err != nil {
		t.Fatalf("resolveAppBuilds: %v", err)
	}
	return builds
}

func releaseBuilds(t *testing.T, cfg Config, manifest *deploymentsv1.Manifest, fingerprint string) appBuilds {
	t.Helper()
	bundles := map[string]appBundle{}
	for _, app := range manifestApps(manifest) {
		bundles[app.GetName()] = appBundle{Fingerprint: fingerprint}
	}
	builds, err := resolveAppBuilds(cfg, manifest, bundles)
	if err != nil {
		t.Fatalf("resolveAppBuilds: %v", err)
	}
	return builds
}

func releaseTokenFor(buildID string) string {
	return releaseOf(buildOnly(buildID)).String()
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
		if !strings.HasSuffix(key, tagSnapshotSuffix) {
			out = append(out, key)
		}
	}
	return out
}

func TestResolveAppBuilds(t *testing.T) {
	t.Parallel()

	t.Run("gives each app its own prefix", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: twoAppTree(t), AssetBucket: "assets", StateTable: "state", Env: "prod"}

		builds, err := resolveAppBuilds(cfg, twoAppManifest(), nil)
		if err != nil {
			t.Fatalf("resolveAppBuilds: %v", err)
		}
		if len(builds.caches) != 2 {
			t.Fatalf("got %d caches, want one per app", len(builds.caches))
		}
		if want := isrPrefixFor("web", "WEB1"); builds.caches["web"].Prefix != want {
			t.Errorf("web prefix = %q, want %q", builds.caches["web"].Prefix, want)
		}
		if want := isrPrefixFor("admin", "ADM1"); builds.caches["admin"].Prefix != want {
			t.Errorf("admin prefix = %q, want %q", builds.caches["admin"].Prefix, want)
		}
	})

	t.Run("omits an app with no prerendered content", func(t *testing.T) {
		t.Parallel()
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

		builds, err := resolveAppBuilds(cfg, manifest, nil)
		if err != nil {
			t.Fatalf("resolveAppBuilds: %v", err)
		}
		if _, ok := builds.caches["api"]; ok {
			t.Errorf("an app with no prerendered content must have no cache, got %+v", builds.caches["api"])
		}
		if builds.caches["web"] == nil {
			t.Error("the Next app must still have its own cache")
		}
		for _, app := range []string{"web", "api"} {
			if builds.bytecode[app] == nil {
				t.Errorf("bytecode cache = nil for %s; every app gets one", app)
			}
		}
	})

	t.Run("a node framework app takes its build id from the serve descriptor", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: nodeAppTree(t), AssetBucket: "assets", StateTable: "state", Env: "prod"}
		manifest := nodeManifest()

		builds := appBuildsFor(t, cfg, manifest)
		if got := builds.identities["api"].BuildID(); got != "a1b2c3d4e5f60718" {
			t.Errorf("build id = %q, want the descriptor's own", got)
		}
		if again := appBuildsFor(t, cfg, manifest).identities["api"].BuildID(); again != builds.identities["api"].BuildID() {
			t.Errorf("build id moved to %q with nothing rebuilt; a no-op deploy must not churn a release", again)
		}
		if builds.caches["api"] != nil {
			t.Errorf("cache = %+v, want none for a framework with no prerendering", builds.caches["api"])
		}
		cache := builds.bytecode["api"]
		if cache == nil {
			t.Fatal("bytecode cache = nil for an express app; the compile cache is not a Next feature")
		}
		if want := bytecodePrefixOf(builds.coords["api"]); cache.Prefix != want {
			t.Errorf("bytecode prefix = %q, want %q", cache.Prefix, want)
		}
		if cache.Bucket != "assets" {
			t.Errorf("bytecode bucket = %q, want the asset bucket", cache.Bucket)
		}
	})

	t.Run("the serve descriptor is authoritative for next too", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"STALE"}`,
			"apps/web/serve.json":            serveDescriptor(t, frameworkNext, "WEB1"),
		})
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", StateTable: "state", Env: "prod"}

		builds := appBuildsFor(t, cfg, nextManifest())
		if got := builds.identities["web"].BuildID(); got != "WEB1" {
			t.Errorf("build id = %q, want the descriptor's own", got)
		}
	})

	t.Run("a serve descriptor with no build id fails naming the app", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{"apps/api/serve.json": `{"framework":"express"}`})
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", StateTable: "state", Env: "prod"}

		_, err := resolveAppBuilds(cfg, nodeManifest(), nil)
		if err == nil {
			t.Fatal("expected a descriptor with no build id to fail the deploy")
		}
		if !strings.Contains(err.Error(), "api") {
			t.Errorf("error must name the app, got %q", err)
		}
	})

	t.Run("an unparseable serve descriptor fails naming the app", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{"apps/api/serve.json": `{`})
		cfg := Config{ArtifactRoot: root, AssetBucket: "assets", StateTable: "state", Env: "prod"}

		_, err := resolveAppBuilds(cfg, nodeManifest(), nil)
		if err == nil {
			t.Fatal("expected an unparseable descriptor to fail the deploy")
		}
		if !strings.Contains(err.Error(), "api") {
			t.Errorf("error must name the app, got %q", err)
		}
	})

	t.Run("an app whose build emitted no descriptor still gets an id", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: t.TempDir(), AssetBucket: "assets", StateTable: "state", Env: "prod"}

		if got := appBuildsFor(t, cfg, nodeManifest()).identities["api"].BuildID(); got == "" {
			t.Error("build id is empty; an app with no artifact must still deploy")
		}
	})
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
			isrKeyFor("admin", "ADM1", "cache/dash.cache.json"),
			isrKeyFor("admin", "ADM1", "cache/users.cache.json"),
			isrKeyFor("web", "WEB1", "cache/index.cache.json"),
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
			isrKeyFor("admin", "ADM1", "cache/dash.cache.json"),
			isrKeyFor("admin", "ADM1", "cache/users.cache.json"),
			isrKeyFor("admin", "ADM1", "tag-clock.json"),
			isrKeyFor("web", "WEB1", "cache/index.cache.json"),
			isrKeyFor("web", "WEB1", "tag-clock.json"),
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

		for _, key := range []string{isrKeyFor("web", "WEB1", "tag-clock.json"), isrKeyFor("admin", "ADM1", "tag-clock.json")} {
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

		for _, key := range []string{isrKeyFor("web", "WEB1", "tag-clock.json"), isrKeyFor("admin", "ADM1", "tag-clock.json")} {
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
		store := &fakeUploader{exists: map[string]bool{isrKeyFor("web", "WEB1", "tag-clock.json"): true}}
		cfg := Config{
			ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader: &fakeUploader{exists: map[string]bool{}}, CacheStoreBucket: "isr", CacheStoreUploader: store,
		}
		cfg = adoptISRWriter(t, cfg)

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if _, ok := store.putBodies[isrKeyFor("web", "WEB1", "tag-clock.json")]; ok {
			t.Error("an existing snapshot was overwritten, want it left as the publisher last wrote it")
		}
		if _, ok := store.putBodies[isrKeyFor("admin", "ADM1", "tag-clock.json")]; !ok {
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
		manifest := &deploymentsv1.Manifest{Slug: "proj"}

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
			isrKeyFor("web", "BID", "cache/blog/post.cache.json"),
			isrKeyFor("web", "BID", "cache/index.cache.json"),
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

		want := isrKeyFor("web", "WEB1", "fetch-cache/") + hash + ".cache.json"
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
