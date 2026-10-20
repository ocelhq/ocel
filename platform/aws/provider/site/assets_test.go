package site

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
)

func staticAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/next-route-table.json":        `{"buildId":"WEB1"}`,
		"apps/web/static/next.svg":              "<svg/>",
		"apps/web/static/_next/static/chunk.js": "console.log(1)",
		"apps/admin/next-route-table.json":      `{"buildId":"ADM1"}`,
		"apps/admin/static/favicon.ico":         "ico",
	})
}

func TestTheRouteTableHoldingABypassTokenIsNotPublishedWithTheStaticFiles(t *testing.T) {
	t.Parallel()

	cfg := assetShippingConfig(t, "web")
	cfg.root = writeTree(t, map[string]string{
		"apps/web/static/logo.svg":            "<svg/>",
		"apps/web/" + edge.OcelRouteTableFile: `{"bypassToken":"secret"}`,
	})
	store := &fakeArtifactStore{exists: map[string]bool{}}
	cfg.buckets.Objects = store
	coord := storageCoordinate("prod", "shop", "web", fixedRelease(t))

	if err := pushStaticAssetSet(context.Background(), cfg, "web", nextStatic, coord); err != nil {
		t.Fatalf("pushStaticAssetSet: %v", err)
	}

	if got := sortedPuts(store); !slices.Equal(got, []string{coord.AssetKey("logo.svg")}) {
		t.Errorf("published %q, want only the one file under %s", got, edge.StaticAssetDir)
	}
}

func sortedPuts(f *fakeArtifactStore) []string {
	keys := append([]string(nil), f.puts...)
	slices.Sort(keys)
	return keys
}

func imageConfigTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/next-route-table.json": `{"buildId":"WEB1"}`,
		"apps/web/image-config.json":     `{"formats":["image/webp"]}`,
		"apps/web/static/logo.png":       "PNG",
	})
}

func edgeReadingConfig(root string, asset *fakeArtifactStore) publication {
	return publication{root: root, buckets: Buckets{Buckets: edgeAndCache, Objects: asset, EdgeHoldsKey: true}}
}

func publishedImageConfig(key string) bool {
	return strings.HasPrefix(key, "image-config/") && strings.HasSuffix(key, "/"+naming.ImageConfigFile)
}

func assetPrefixFor(app, buildID string) string {
	return strings.TrimSuffix(kindKeyFor("assets", app, buildID, ""), "/")
}

func assetKeyFor(app, buildID, rest string) string {
	return assetPrefixFor(app, buildID) + "/" + rest
}

func imageConfigKeyFor(app, buildID string) string {
	return kindKeyFor("image-config", app, buildID, "image-config.json")
}

func TestUploadStaticAssets(t *testing.T) {
	t.Parallel()

	t.Run("uploads each app under its own prefix", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(staticAppTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		got := append([]string(nil), asset.puts...)
		slices.Sort(got)
		want := []string{
			assetKeyFor("admin", testBuildID, "favicon.ico"),
			assetKeyFor("web", testBuildID, "_next/static/chunk.js"),
			assetKeyFor("web", testBuildID, "next.svg"),
		}
		if len(got) != len(want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("uploaded key[%d] = %q, want %q", i, got[i], want[i])
			}
		}
		for _, b := range asset.buckets {
			if b != "edge" {
				t.Errorf("uploaded into bucket %q, want the account's edge bucket %q", b, "edge")
			}
		}
	})

	t.Run("uploads for an edge that reads the bucket by its policy and holds no key", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: staticAppTree(t), buckets: Buckets{Buckets: edgeAndCache, Objects: asset}}

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		if got := sortedPuts(asset); !slices.Contains(got, assetKeyFor("web", testBuildID, "_next/static/chunk.js")) {
			t.Errorf("uploaded %v, want the static files a CloudFront edge serves from the bucket", got)
		}
	})

	t.Run("stamps content type and cache control per path tier", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json": `{"buildId":"WEB1"}`,
			"apps/web/hosting.json": `{"version":1,"framework":"next","frameworkBuildId":"WEB1","rootFunction":"/","routeTable":"next","static":{` +
				`"immutablePrefixes":["/_next/static/","/docs/_next/static/"],` +
				`"mustRevalidatePrefixes":["/_next/static/service-worker/","/docs/_next/static/service-worker/"]}}`,
			"apps/web/static/_next/static/chunk.js":                  "console.log(1)",
			"apps/web/static/_next/static/css/app.css":               "body{}",
			"apps/web/static/_next/static/service-worker/sw.js":      "self",
			"apps/web/static/docs/_next/static/chunks/main.js":       "console.log(2)",
			"apps/web/static/docs/_next/static/service-worker/sw.js": "self",
			"apps/web/static/next.svg":                               "<svg/>",
			"apps/web/static/styles.css":                             "body{}",
			"apps/web/static/chunk.js.map":                           `{"version":3}`,
			"apps/web/static/favicon.ico":                            "icon",
			"apps/web/static/robots.txt":                             "User-agent: *",
			"apps/web/static/manifest.json":                          `{"name":"web"}`,
			"apps/web/static/index.html":                             "<html></html>",
			"apps/web/static/font.woff2":                             "woff",
			"apps/web/static/LICENSE":                                "MIT",
		})
		cfg := edgeReadingConfig(root, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		for _, tc := range []struct{ rel, contentType, cacheControl string }{
			{"_next/static/chunk.js", "text/javascript; charset=utf-8", edge.ImmutableCacheControl},
			{"_next/static/css/app.css", "text/css; charset=utf-8", edge.ImmutableCacheControl},
			{"_next/static/service-worker/sw.js", "text/javascript; charset=utf-8", edge.RevalidateCacheControl},
			{"docs/_next/static/chunks/main.js", "text/javascript; charset=utf-8", edge.ImmutableCacheControl},
			{"docs/_next/static/service-worker/sw.js", "text/javascript; charset=utf-8", edge.RevalidateCacheControl},
			{"next.svg", "image/svg+xml", edge.RevalidateCacheControl},
			{"styles.css", "text/css; charset=utf-8", edge.RevalidateCacheControl},
			{"chunk.js.map", "application/json; charset=utf-8", edge.RevalidateCacheControl},
			{"favicon.ico", "image/x-icon", edge.RevalidateCacheControl},
			{"robots.txt", "text/plain", edge.RevalidateCacheControl},
			{"manifest.json", "application/manifest+json", edge.RevalidateCacheControl},
			{"index.html", "text/html; charset=utf-8", edge.RevalidateCacheControl},
			{"font.woff2", "font/woff2", edge.RevalidateCacheControl},
			{"LICENSE", "application/octet-stream", edge.RevalidateCacheControl},
		} {
			key := assetKeyFor("web", testBuildID, tc.rel)
			if got := asset.contentTypes[key]; got != tc.contentType {
				t.Errorf("%s content-type = %q, want %q", tc.rel, got, tc.contentType)
			}
			if got := asset.cacheControls[key]; got != tc.cacheControl {
				t.Errorf("%s cache-control = %q, want %q", tc.rel, got, tc.cacheControl)
			}
		}
	})

	t.Run("a present object is not re-put", func(t *testing.T) {
		t.Parallel()
		key := assetKeyFor("web", testBuildID, "_next/static/chunk.js")
		asset := &fakeArtifactStore{exists: map[string]bool{key: true}}
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json":        `{"buildId":"WEB1"}`,
			"apps/web/static/_next/static/chunk.js": "console.log(1)",
		})
		cfg := edgeReadingConfig(root, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		if got := sortedPuts(asset); len(got) != 0 {
			t.Errorf("edge bucket put %v, want the HEAD hit to skip the upload", got)
		}
	})

	t.Run("no static output uploads nothing", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json": `{"buildId":"WEB1"}`,
		})
		cfg := edgeReadingConfig(root, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		if len(asset.puts) != 0 {
			t.Errorf("uploaded %v, want nothing for an app with no static output", asset.puts)
		}
	})

	t.Run("a rotation reuses the build's objects", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(staticAppTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("first uploadStaticAssets: %v", err)
		}
		first := append([]string(nil), asset.puts...)
		if len(first) == 0 {
			t.Fatal("first deploy uploaded nothing; there is no reuse to prove")
		}
		for _, key := range first {
			asset.exists[key] = true
		}
		asset.puts = nil

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("rotation uploadStaticAssets: %v", err)
		}
		if len(asset.puts) != 0 {
			t.Errorf("rotation uploaded %v, want nothing: the build's assets are already published", asset.puts)
		}
	})

	t.Run("publishes the assets and the image config into the edge bucket alone", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(imageConfigTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		want := []string{assetKeyFor("web", testBuildID, "logo.png"), imageConfigKeyFor("web", testBuildID)}
		slices.Sort(want)
		if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
			t.Errorf("edge bucket keys = %v, want %v", got, want)
		}
		for _, b := range asset.buckets {
			if b != "edge" {
				t.Errorf("uploaded into bucket %q, want the account's own %q", b, "edge")
			}
		}
	})

	t.Run("publishes the image config outside the public web root", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(imageConfigTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		key := imageConfigKeyFor("web", testBuildID)
		if got, want := asset.putBodies[key], `{"formats":["image/webp"]}`; got != want {
			t.Errorf("image config bytes = %q, want %q — the origin hashes exactly these", got, want)
		}
		if got, want := asset.contentTypes[key], "application/json"; got != want {
			t.Errorf("image config content-type = %q, want %q", got, want)
		}
	})

	t.Run("a project's own image config asset does not collide", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/next-route-table.json":    `{"buildId":"WEB1"}`,
			"apps/web/image-config.json":        `{"formats":["image/webp"]}`,
			"apps/web/static/image-config.json": `{"mine":true}`,
		})
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(root, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		if got, want := asset.putBodies[assetKeyFor("web", testBuildID, "image-config.json")], `{"mine":true}`; got != want {
			t.Errorf("the project's own public/image-config.json = %q, want %q", got, want)
		}
		if got, want := asset.putBodies[imageConfigKeyFor("web", testBuildID)], `{"formats":["image/webp"]}`; got != want {
			t.Errorf("compiled image config = %q, want %q", got, want)
		}
	})

	t.Run("app without an image config publishes none", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeReadingConfig(staticAppTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		for _, key := range sortedPuts(asset) {
			if publishedImageConfig(key) {
				t.Errorf("published %q for a build that emitted no image config", key)
			}
		}
	})

	t.Run("republishes the image config over a present object", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{
			imageConfigKeyFor("web", testBuildID):       true,
			assetKeyFor("web", testBuildID, "logo.png"): true,
		}
		asset := &fakeArtifactStore{exists: present}
		cfg := edgeReadingConfig(imageConfigTree(t), asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		want := []string{imageConfigKeyFor("web", testBuildID)}
		if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
			t.Errorf("edge bucket keys = %v, want %v", got, want)
		}
	})

	t.Run("the edge bucket failing fails the deploy", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("bucket is on fire")
		for _, tc := range []struct {
			name  string
			asset *fakeArtifactStore
		}{
			{"put fails", &fakeArtifactStore{exists: map[string]bool{}, putErr: boom}},
			{"head fails", &fakeArtifactStore{exists: map[string]bool{}, headErr: boom}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := edgeReadingConfig(imageConfigTree(t), tc.asset)

				err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest()))
				if err == nil {
					t.Fatal("uploadStaticAssets = nil, want the failed bucket to fail the deploy")
				}
				if !errors.Is(err, boom) {
					t.Errorf("err = %v, want it to wrap what the bucket said", err)
				}
			})
		}
	})
}
