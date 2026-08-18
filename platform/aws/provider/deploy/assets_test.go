package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

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

func sortedPuts(f *fakeUploader) []string {
	keys := append([]string(nil), f.puts...)
	slices.Sort(keys)
	return keys
}

func imageConfigTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		"apps/web/image-config.json":     `{"formats":["image/webp"]}`,
		"apps/web/static/logo.png":       "PNG",
	})
}

func mirrorConfig(root string, store, asset *fakeUploader) Config {
	return Config{
		ArtifactRoot: root, Env: "prod",
		AssetBucket: "assets", Uploader: asset,
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
}

func publishedImageConfig(key string) bool {
	return strings.HasSuffix(key, "/"+imageConfigFile) && !strings.Contains(key, "/assets/")
}

func assetPrefixFor(app, deploymentID string) string {
	return storagePrefixFor("prod", "proj", app, deploymentID) + "assets"
}

func assetKeyFor(app, deploymentID, rest string) string {
	return assetPrefixFor(app, deploymentID) + "/" + rest
}

func imageConfigKeyFor(app, deploymentID string) string {
	return storagePrefixFor("prod", "proj", app, deploymentID) + "image-config.json"
}

func TestAppAssetPrefix(t *testing.T) {
	t.Parallel()
	got := appAssetPrefix(storageCoordinate("prod", "proj", "web", releaseOf(deployedAs(testDeploymentID))))
	want := assetPrefixFor("web", testDeploymentID)
	if got != want {
		t.Errorf("appAssetPrefix = %q, want %q", got, want)
	}
	if config := imageConfigKeyFor("web", testDeploymentID); !strings.HasPrefix(config, strings.TrimSuffix(got, "assets")) {
		t.Errorf("image config %q does not sit beside the assets under one release prefix %q", config, got)
	}
}

func TestUploadStaticAssets(t *testing.T) {
	t.Parallel()

	t.Run("uploads each app under its own prefix", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{
			assetKeyFor("admin", testDeploymentID, "favicon.ico"),
			assetKeyFor("web", testDeploymentID, "_next/static/chunk.js"),
			assetKeyFor("web", testDeploymentID, "next.svg"),
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

	t.Run("stamps content type and cache control per path class", func(t *testing.T) {
		t.Parallel()
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":                         `{"buildId":"WEB1"}`,
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
		cfg := mirrorConfig(root, store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		for _, tc := range []struct{ rel, contentType, cacheControl string }{
			{"_next/static/chunk.js", "text/javascript; charset=utf-8", immutableCacheControl},
			{"_next/static/css/app.css", "text/css; charset=utf-8", immutableCacheControl},
			{"_next/static/service-worker/sw.js", "text/javascript; charset=utf-8", revalidateCacheControl},
			{"docs/_next/static/chunks/main.js", "text/javascript; charset=utf-8", immutableCacheControl},
			{"docs/_next/static/service-worker/sw.js", "text/javascript; charset=utf-8", revalidateCacheControl},
			{"next.svg", "image/svg+xml", revalidateCacheControl},
			{"styles.css", "text/css; charset=utf-8", revalidateCacheControl},
			{"chunk.js.map", "application/json; charset=utf-8", revalidateCacheControl},
			{"favicon.ico", "image/x-icon", revalidateCacheControl},
			{"robots.txt", "text/plain", revalidateCacheControl},
			{"manifest.json", "application/manifest+json", revalidateCacheControl},
			{"index.html", "text/html; charset=utf-8", revalidateCacheControl},
			{"font.woff2", "font/woff2", revalidateCacheControl},
			{"LICENSE", "application/octet-stream", revalidateCacheControl},
		} {
			key := assetKeyFor("web", testDeploymentID, tc.rel)
			for name, up := range map[string]*fakeUploader{"cache store": store, "asset bucket": asset} {
				if got := up.contentTypes[key]; got != tc.contentType {
					t.Errorf("%s %s content-type = %q, want %q", name, tc.rel, got, tc.contentType)
				}
				if got := up.cacheControls[key]; got != tc.cacheControl {
					t.Errorf("%s %s cache-control = %q, want %q", name, tc.rel, got, tc.cacheControl)
				}
			}
		}
	})

	t.Run("a present object is not re-put", func(t *testing.T) {
		t.Parallel()
		key := assetKeyFor("web", testDeploymentID, "_next/static/chunk.js")
		store := &fakeUploader{exists: map[string]bool{key: true}}
		asset := &fakeUploader{exists: map[string]bool{key: true}}
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":        `{"buildId":"WEB1"}`,
			"apps/web/static/_next/static/chunk.js": "console.log(1)",
		})
		cfg := mirrorConfig(root, store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		for name, up := range map[string]*fakeUploader{"cache store": store, "asset bucket": asset} {
			if got := sortedPuts(up); len(got) != 0 {
				t.Errorf("%s put %v, want the HEAD hit to skip the upload", name, got)
			}
		}
	})

	t.Run("unadopted store uploads nothing", func(t *testing.T) {
		t.Parallel()
		asset := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset}

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		if len(asset.puts) != 0 {
			t.Errorf("asset bucket received %v, want nothing with no adopted store", asset.puts)
		}
	})

	t.Run("no static output uploads nothing", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		})
		cfg := Config{
			ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		if len(store.puts) != 0 {
			t.Errorf("uploaded %v, want nothing for an app with no static output", store.puts)
		}
	})

	t.Run("a rotation reuses the build's objects", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("first uploadStaticAssets: %v", err)
		}
		first := append([]string(nil), store.puts...)
		if len(first) == 0 {
			t.Fatal("first deploy uploaded nothing; there is no reuse to prove")
		}
		for _, key := range first {
			store.exists[key] = true
		}
		store.puts = nil

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("rotation uploadStaticAssets: %v", err)
		}
		if len(store.puts) != 0 {
			t.Errorf("rotation uploaded %v, want nothing: the build's assets are already published", store.puts)
		}
	})

	t.Run("mirrors identical keys and bytes to both targets", func(t *testing.T) {
		t.Parallel()
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		cfg := mirrorConfig(imageConfigTree(t), store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		key := assetKeyFor("web", testDeploymentID, "logo.png")
		if got := sortedPuts(store); !reflect.DeepEqual(got, []string{key}) {
			t.Errorf("cache store keys = %v, want %v", got, []string{key})
		}
		want := []string{key, imageConfigKeyFor("web", testDeploymentID)}
		if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
			t.Errorf("asset bucket keys = %v, want %v", got, want)
		}
		if store.putBodies[key] != asset.putBodies[key] {
			t.Errorf("mirrored bodies differ: store = %q, asset = %q", store.putBodies[key], asset.putBodies[key])
		}
		for _, b := range asset.buckets {
			if b != "assets" {
				t.Errorf("mirrored into bucket %q, want the account's own %q", b, "assets")
			}
		}
	})

	t.Run("publishes the image config outside the public web root", func(t *testing.T) {
		t.Parallel()
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		cfg := mirrorConfig(imageConfigTree(t), store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		key := imageConfigKeyFor("web", testDeploymentID)
		if got, want := asset.putBodies[key], `{"formats":["image/webp"]}`; got != want {
			t.Errorf("image config bytes = %q, want %q — the origin hashes exactly these", got, want)
		}
		if got, want := asset.contentTypes[key], "application/json"; got != want {
			t.Errorf("image config content-type = %q, want %q", got, want)
		}
		if _, published := store.putBodies[key]; published {
			t.Errorf("published %q to the cache store, want the asset bucket alone", key)
		}
		for _, k := range sortedPuts(store) {
			if publishedImageConfig(k) {
				t.Errorf("cache store received %q, want no image config in R2 at all", k)
			}
		}
	})

	t.Run("a project's own image config asset does not collide", func(t *testing.T) {
		t.Parallel()
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":    `{"buildId":"WEB1"}`,
			"apps/web/image-config.json":        `{"formats":["image/webp"]}`,
			"apps/web/static/image-config.json": `{"mine":true}`,
		})
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		cfg := mirrorConfig(root, store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		if got, want := asset.putBodies[assetKeyFor("web", testDeploymentID, "image-config.json")], `{"mine":true}`; got != want {
			t.Errorf("the project's own public/image-config.json = %q, want %q", got, want)
		}
		if got, want := asset.putBodies[imageConfigKeyFor("web", testDeploymentID)], `{"formats":["image/webp"]}`; got != want {
			t.Errorf("compiled image config = %q, want %q", got, want)
		}
	})

	t.Run("app without an image config publishes none", func(t *testing.T) {
		t.Parallel()
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		cfg := mirrorConfig(staticAppTree(t), store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}
		for _, key := range append(sortedPuts(store), sortedPuts(asset)...) {
			if publishedImageConfig(key) {
				t.Errorf("published %q for a build that emitted no image config", key)
			}
		}
	})

	t.Run("republishes the image config over a present object", func(t *testing.T) {
		t.Parallel()
		present := map[string]bool{
			imageConfigKeyFor("web", testDeploymentID):       true,
			assetKeyFor("web", testDeploymentID, "logo.png"): true,
		}
		store := &fakeUploader{exists: present}
		asset := &fakeUploader{exists: present}
		cfg := mirrorConfig(imageConfigTree(t), store, asset)

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadStaticAssets: %v", err)
		}

		if got := sortedPuts(store); got != nil {
			t.Errorf("cache store keys = %v, want nothing re-put", got)
		}
		want := []string{imageConfigKeyFor("web", testDeploymentID)}
		if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
			t.Errorf("asset bucket keys = %v, want %v", got, want)
		}
	})

	t.Run("either target failing fails the deploy", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("bucket is on fire")
		for _, tc := range []struct {
			name         string
			store, asset *fakeUploader
		}{
			{"cache store put fails", &fakeUploader{exists: map[string]bool{}, putErr: boom}, &fakeUploader{exists: map[string]bool{}}},
			{"asset bucket put fails", &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}, putErr: boom}},
			{"asset bucket head fails", &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}, headErr: boom}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := mirrorConfig(imageConfigTree(t), tc.store, tc.asset)

				err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest()))
				if err == nil {
					t.Fatal("uploadStaticAssets = nil, want the failed target to fail the deploy")
				}
				if !errors.Is(err, boom) {
					t.Errorf("err = %v, want it to carry what the bucket said", err)
				}
			})
		}
	})

	t.Run("missing asset bucket fails the deploy", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: imageConfigTree(t), Env: "prod",
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadStaticAssets(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err == nil {
			t.Fatal("uploadStaticAssets = nil, want an error for a missing asset bucket")
		}
	})
}

func TestUploadPrerenderAssetsMirroring(t *testing.T) {
	t.Run("route entries are not mirrored to the asset bucket", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json":     `{"buildId":"WEB1"}`,
			"apps/web/cache/index.cache.json":    `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
			"apps/web/fetch-cache/a1.cache.json": `{"lastModified":2,"value":{"kind":"FETCH"}}`,
		})
		store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
		cfg := adoptISRWriter(t, mirrorConfig(root, store, asset))

		if err := uploadPrerenderAssets(context.Background(), cfg, appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadPrerenderAssets: %v", err)
		}

		entry := isrKeyFor("web", testDeploymentID, "cache/index.cache.json")
		if got := sortedPuts(store); !reflect.DeepEqual(got, []string{entry, isrKeyFor("web", testDeploymentID, "tag-clock.json")}) {
			t.Errorf("cache store keys = %v, want the route entry and the tag clock", got)
		}
		for _, key := range sortedPuts(asset) {
			if key == entry {
				t.Errorf("route entry %q was mirrored to the asset bucket, want it R2-only", key)
			}
		}
	})
}

func TestBuildDeploymentRecordAssets(t *testing.T) {
	t.Run("asset prefix is the full R2 key root", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		})
		cfg := Config{ArtifactRoot: root, Env: "prod"}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

		record, err := buildDeploymentRecord(cfg, manifest, app, deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if want := assetPrefixFor("web", testDeploymentID); record.AssetPrefix != want {
			t.Errorf("AssetPrefix = %q, want %q", record.AssetPrefix, want)
		}
	})

	t.Run("ISR prefix is the ISR key root", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		})
		cfg := Config{ArtifactRoot: root, Env: "prod"}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

		record, err := buildDeploymentRecord(cfg, manifest, app, deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if want := isrPrefixFor("web", testDeploymentID); record.IsrPrefix != want {
			t.Errorf("IsrPrefix = %q, want %q", record.IsrPrefix, want)
		}
	})

	t.Run("non-Next app has no asset prefix", func(t *testing.T) {
		cfg := Config{ArtifactRoot: t.TempDir()}
		manifest := &deploymentsv1.Manifest{Slug: "proj"}
		app := &deploymentsv1.ManifestApp{Name: "api", Framework: "express"}

		record, err := buildDeploymentRecord(cfg, manifest, app, deployedAs("API1"), nil, appBuildsFor(t, cfg, manifest), nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if record.AssetPrefix != "" {
			t.Errorf("AssetPrefix = %q, want empty for a non-Next app", record.AssetPrefix)
		}
	})

	t.Run("carries the build's write secret", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		})
		cfg := Config{
			ArtifactRoot:           root,
			Env:                    "prod",
			CacheStoreBucket:       "ocel-edge-cache",
			CacheStoreUploader:     &fakeUploader{exists: map[string]bool{}},
			ISRWriterEndpoint:      "https://writer.example",
			ISRWriterBootstrapCred: "boot",
			ISRWriterSeed:          "seed-1",
		}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

		record, err := buildDeploymentRecord(cfg, manifest, app, deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if want := isrWriteSecret("seed-1", isrPrefixFor("web", testDeploymentID)); record.IsrWriteSecret != want {
			t.Errorf("IsrWriteSecret = %q, want the secret derived for this build's prefix", record.IsrWriteSecret)
		}
	})

	t.Run("no writer leaves no write secret", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		})
		cfg := Config{ArtifactRoot: root, Env: "prod"}
		record, err := buildDeploymentRecord(cfg, nextManifest(), &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}, deployedAs("WEB1"), nil, appBuildsFor(t, cfg, nextManifest()), nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if record.IsrWriteSecret != "" {
			t.Errorf("IsrWriteSecret = %q, want empty", record.IsrWriteSecret)
		}
	})
}
