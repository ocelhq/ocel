package site

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
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
		"apps/web/next-route-table.json":    `{"buildId":"WEB1"}`,
		"apps/web/cache/index.cache.json":   `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/admin/next-route-table.json":  `{"buildId":"ADM1"}`,
		"apps/admin/cache/dash.cache.json":  `{"lastModified":2,"value":{"kind":"APP_PAGE"}}`,
		"apps/admin/cache/users.cache.json": `{"lastModified":3,"value":{"kind":"APP_PAGE"}}`,
	})
}

type publication struct {
	root    string
	buckets Buckets
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
	caches map[string]*ISR
	baked  map[string]variables.Bundle
}

func appBuildsFor(t *testing.T, cfg publication, manifest *contractv1.Manifest) appBuilds {
	t.Helper()
	return bakedBuilds(t, cfg, manifest, nil)
}

func bakedBuilds(t *testing.T, cfg publication, manifest *contractv1.Manifest, baked map[string]variables.Bundle) appBuilds {
	t.Helper()
	env, manifest := stackrecords.ProductionEnv, deployedManifest(manifest)
	builds := appBuilds{
		coords: map[string]naming.Coordinate{},
		caches: map[string]*ISR{},
		baked:  baked,
	}
	if builds.baked == nil {
		builds.baked = map[string]variables.Bundle{}
	}
	for _, app := range manifest.GetApps() {
		name := app.GetName()
		id, err := provider.NewRelease(app.GetBuildId(), "p1", env, builds.baked[name].Fingerprint)
		if err != nil {
			t.Fatalf("release for %s: %v", name, err)
		}
		coord := storageCoordinate(env, manifest.GetSlug(), name, id.Token())
		builds.coords[name] = coord
		if app.GetFramework().GetName() != buildoutput.FrameworkNext {
			continue
		}
		cache := &ISR{
			Coord:   coord,
			Buckets: cfg.buckets.Buckets,
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

func pushSet(ctx context.Context, set *AssetSet, err error) error {
	if err != nil || set == nil {
		return err
	}
	return set.push(ctx, quietProgress{})
}

func pushStaticAssetSet(ctx context.Context, cfg publication, app string, static *edge.Static, coord naming.Coordinate) error {
	set, err := staticAssetSet(cfg.buckets, cfg.root, app, static, coord)
	return pushSet(ctx, set, err)
}

func uploadStaticAssets(ctx context.Context, cfg publication, manifest *contractv1.Manifest, builds appBuilds) error {
	for _, app := range deployedManifest(manifest).GetApps() {
		name := app.GetName()
		hosting, _, err := buildoutput.ReadHosting(cfg.root, name)
		if err != nil {
			return err
		}
		if err := pushStaticAssetSet(ctx, cfg, name, hosting.Static, builds.coords[name]); err != nil {
			return err
		}
	}
	return nil
}

func uploadPrerenderAssets(ctx context.Context, cfg publication, builds appBuilds) error {
	for _, name := range slices.Sorted(maps.Keys(builds.coords)) {
		set, err := prerenderAssetSet(cfg.buckets, cfg.root, name, builds.caches[name])
		if err := pushSet(ctx, set, err); err != nil {
			return err
		}
	}
	return nil
}

func uploadEdgeBundles(ctx context.Context, cfg publication, manifest *contractv1.Manifest, builds appBuilds) error {
	for _, app := range deployedManifest(manifest).GetApps() {
		name := app.GetName()
		set, _, err := edgeBundleSet(cfg.buckets, cfg.root, name, builds.coords[name], builds.baked[name])
		if err := pushSet(ctx, set, err); err != nil {
			return err
		}
	}
	return nil
}

func releaseTokenFor(buildID string) string {
	return deployedAs(buildID).Token().String()
}

func kindKeyFor(kind, app, buildID, rest string) string {
	return kind + "/proj/prod/" + app + "/" + releaseTokenFor(buildID) + "/" + rest
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

func TestEachReleasesPrerenderSetDiffersEvenWithNoEntriesSoEachReleaseSeedsItsTagClock(t *testing.T) {
	root := writeTree(t, map[string]string{"apps/web/next-route-table.json": `{"buildId":"BID","appName":"web"}`})
	b := Buckets{Buckets: edgeAndCache, Objects: &fakeArtifactStore{exists: map[string]bool{}}}
	digests := map[string]string{}
	for _, release := range []string{"r1a2b3c4d", "r5e6f7a8b"} {
		token, err := naming.ParseReleaseToken(release)
		if err != nil {
			t.Fatal(err)
		}
		coord := naming.Coordinate{Project: "proj", Env: "prod", App: "web", Release: token}
		set, err := prerenderAssetSet(b, root, "web", &ISR{Coord: coord, Buckets: b.Buckets})
		if err != nil || set == nil {
			t.Fatalf("prerenderAssetSet(%s) = %v, %v, want a set that seeds the tag clock", release, set, err)
		}
		digests[set.digest] = release
	}
	if len(digests) != 2 {
		t.Errorf("two releases with no prerendered entries share the prerender set digest %v: the set is pushed only when its inputs change, so the second release never seeds its own tag clock", digests)
	}
}

func TestUploadPrerenderAssets(t *testing.T) {
	t.Parallel()

	t.Run("uploads each app under its own prefix", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: f}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			kindKeyFor("pages", "admin", testBuildID, "dash.cache.json"),
			kindKeyFor("pages", "admin", testBuildID, "users.cache.json"),
			kindKeyFor("pages", "web", testBuildID, "index.cache.json"),
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

	t.Run("seeds the page entries into the edge bucket and the tag snapshots into the cache bucket", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: asset, EdgeHoldsKey: true}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := append([]string(nil), asset.puts...)
		slices.Sort(got)
		want := []string{
			kindKeyFor("pages", "admin", testBuildID, "dash.cache.json"),
			kindKeyFor("pages", "admin", testBuildID, "users.cache.json"),
			kindKeyFor("pages", "web", testBuildID, "index.cache.json"),
			kindKeyFor("tag-clock", "admin", testBuildID, "tag-clock.json"),
			kindKeyFor("tag-clock", "web", testBuildID, "tag-clock.json"),
		}
		if !slices.Equal(got, want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		for i, key := range asset.puts {
			want := "edge"
			if strings.HasPrefix(key, "tag-clock/") {
				want = "cache"
			}
			if asset.buckets[i] != want {
				t.Errorf("%s went to bucket %q, want %q", key, asset.buckets[i], want)
			}
		}
	})

	t.Run("an edge holding no key to the edge bucket still finds the page entries in it", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: f}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if len(entryPuts(f.puts)) != 3 {
			t.Fatalf("uploaded %v, want the three cache entries", entryPuts(f.puts))
		}
		for i, key := range f.puts {
			if strings.HasPrefix(key, "pages/") && f.buckets[i] != "edge" {
				t.Errorf("%s went to bucket %q, want the edge bucket", key, f.buckets[i])
			}
		}
	})

	t.Run("seeds the genesis tag snapshot", func(t *testing.T) {
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: store}}

		before := time.Now().UnixMilli()
		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}
		after := time.Now().UnixMilli()

		for _, key := range []string{kindKeyFor("tag-clock", "web", testBuildID, "tag-clock.json"), kindKeyFor("tag-clock", "admin", testBuildID, "tag-clock.json")} {
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

	t.Run("keeps an existing snapshot", func(t *testing.T) {
		t.Parallel()
		store := &fakeArtifactStore{exists: map[string]bool{kindKeyFor("tag-clock", "web", testBuildID, "tag-clock.json"): true}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: store}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		if _, ok := store.putBodies[kindKeyFor("tag-clock", "web", testBuildID, "tag-clock.json")]; ok {
			t.Error("an existing snapshot was overwritten, want it left as the publisher last wrote it")
		}
		if _, ok := store.putBodies[kindKeyFor("tag-clock", "admin", testBuildID, "tag-clock.json")]; !ok {
			t.Error("the other app's snapshot was not seeded; one refusal must not stop the rest")
		}
	})

	t.Run("seeds one snapshot per app", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: twoAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: f}}

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
			t.Errorf("seeded %d snapshots, want one per app", seeded)
		}
	})

	t.Run("no Next app", func(t *testing.T) {
		t.Parallel()
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: t.TempDir(), buckets: Buckets{Buckets: edgeAndCache, Objects: f}}
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
			"apps/web/next-route-table.json":                     `{"buildId":"BID","appName":"web"}`,
			"apps/web/functions/index.func/function-config.json": `{"id":"/"}`,
		})
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: root, buckets: Buckets{Buckets: edgeAndCache, Objects: f}}

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
			"apps/web/next-route-table.json":  `{"buildId":"BID","appName":"web"}`,
			"apps/web/cache/index.cache.json": `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		})
		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: root, buckets: Buckets{Objects: f}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err == nil {
			t.Fatal("uploadPrerenderAssets = nil, want an error for a missing edge bucket")
		}
	})

	t.Run("uploads cache entries", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json":      `{"buildId":"BID","appName":"web"}`,
			"apps/web/cache/index.cache.json":     `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
			"apps/web/cache/blog/post.cache.json": `{"lastModified":2,"value":{"kind":"APP_PAGE"}}`,
		})

		f := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: root, buckets: Buckets{Buckets: edgeAndCache, Objects: f}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		got := entryPuts(f.puts)
		slices.Sort(got)
		want := []string{
			kindKeyFor("pages", "web", testBuildID, "blog/post.cache.json"),
			kindKeyFor("pages", "web", testBuildID, "index.cache.json"),
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

	t.Run("a fetch entry lands in the cache bucket beside a page in the edge bucket", func(t *testing.T) {
		t.Parallel()
		hash := "a1b2c3"
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json":               `{"buildId":"WEB1"}`,
			"apps/web/cache/index.cache.json":              `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
			"apps/web/fetch-cache/" + hash + ".cache.json": `{"lastModified":2,"value":{"kind":"FETCH"}}`,
		})
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: root, buckets: Buckets{Buckets: edgeAndCache, Objects: asset, EdgeHoldsKey: true}}

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		fetch := kindKeyFor("fetch-cache", "web", testBuildID, "") + hash + ".cache.json"
		at := slices.Index(asset.puts, fetch)
		if at < 0 {
			t.Fatalf("the buckets got %v, want the fetch entry %s", asset.puts, fetch)
		}
		if asset.buckets[at] != "cache" {
			t.Errorf("fetch entry %s went to bucket %q, want the cache bucket: only the app's own code reads it", fetch, asset.buckets[at])
		}
	})
}
