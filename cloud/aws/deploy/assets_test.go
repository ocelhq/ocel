package deploy

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// staticAppTree seeds two Next apps' build output, each with its own build id
// and its own static/ directory.
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

// TestAppAssetR2Prefix pins the ADR 0002 key layout: assets/<project>/<app>/
// <build id>, disjoint from the isr cache-entry prefix.
func TestAppAssetR2Prefix(t *testing.T) {
	got := appAssetR2Prefix("proj", "web", "WEB1")
	want := "assets/proj/web/WEB1"
	if got != want {
		t.Errorf("appAssetR2Prefix = %q, want %q", got, want)
	}
}

// TestUploadStaticAssets_UploadsEachAppUnderItsOwnPrefix proves each app's
// static/ output lands under its own assets/<project>/<app>/<build id>
// prefix in the adopted cache store, so a rollback (which swaps the pointer,
// not the objects) can address an older build's assets by that same key.
func TestUploadStaticAssets_UploadsEachAppUnderItsOwnPrefix(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	got := append([]string(nil), store.puts...)
	sort.Strings(got)
	want := []string{
		"assets/proj/admin/ADM1/favicon.ico",
		"assets/proj/web/WEB1/_next/static/chunk.js",
		"assets/proj/web/WEB1/next.svg",
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

// TestUploadStaticAssets_StampsNoContentType proves no static object is written
// with a content-type. The frozen worker derives one from the name the build
// emitted the file under, mirroring what Next.js serves; a stamp here would
// come from the deploy host's own mime database — which answers
// image/vnd.microsoft.icon where Next serves image/x-icon, and answers
// differently on two hosts — and would be a second, contradicting source of
// truth.
func TestUploadStaticAssets_StampsNoContentType(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":        `{"buildId":"WEB1"}`,
		"apps/web/static/next.svg":              "<svg/>",
		"apps/web/static/_next/static/chunk.js": "console.log(1)",
		"apps/web/static/styles.css":            "body{}",
		"apps/web/static/chunk.js.map":          `{"version":3}`,
		"apps/web/static/favicon.ico":           "icon",
	})
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	if len(store.contentTypes) != 0 {
		t.Errorf("content-types = %v, want none — the worker names the type", store.contentTypes)
	}
}

// TestUploadStaticAssets_UnadoptedStoreUploadsNothing proves a substrate whose
// edge offered no cache store uploads no assets at all: there is nowhere for
// the frozen worker to read them back from, so uploading into the provider's
// own asset bucket would only be dead weight.
func TestUploadStaticAssets_UnadoptedStoreUploadsNothing(t *testing.T) {
	asset := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset}

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	if len(asset.puts) != 0 {
		t.Errorf("asset bucket received %v, want nothing with no adopted store", asset.puts)
	}
}

// TestUploadStaticAssets_NoStaticOutputUploadsNothing proves an app with no
// static/ directory (a pure API app, say) is a no-op rather than an error.
func TestUploadStaticAssets_NoStaticOutputUploadsNothing(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	if len(store.puts) != 0 {
		t.Errorf("uploaded %v, want nothing for an app with no static output", store.puts)
	}
}

// TestUploadStaticAssets_ARotationReusesTheBuildsObjects proves a vars-only
// deploy re-publishing an unchanged build re-uploads nothing: the keys are
// build-scoped and already present, so a rotation costs presence checks alone
// rather than the whole static tree again.
func TestUploadStaticAssets_ARotationReusesTheBuildsObjects(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: staticAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
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

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("rotation uploadStaticAssets: %v", err)
	}
	if len(store.puts) != 0 {
		t.Errorf("rotation uploaded %v, want nothing: the build's assets are already published", store.puts)
	}
}

// sortedPuts is one uploader's recorded keys in a comparable order — the
// uploads fan out concurrently, so only the set is meaningful.
func sortedPuts(f *fakeUploader) []string {
	keys := append([]string(nil), f.puts...)
	sort.Strings(keys)
	return keys
}

// imageConfigTree seeds one Next app whose build emitted a compiled image
// config beside its routing manifest, as an app with optimizable images does.
func imageConfigTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
		"apps/web/image-config.json":     `{"formats":["image/webp"]}`,
		"apps/web/static/logo.png":       "PNG",
	})
}

// mirrorConfig is the two-target asset-plane Config: the adopted R2 store the
// worker reads and the account's own S3 bucket the image optimizer reads.
func mirrorConfig(root string, store, asset *fakeUploader) Config {
	return Config{
		ArtifactRoot: root, Env: "prod",
		AssetBucket: "assets", Uploader: asset,
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
}

// TestUploadStaticAssets_MirrorsIdenticalKeysAndBytesToBothTargets proves the
// static assets are published twice under one key layout: R2 is the hot tier the
// worker reads, and the account's own bucket is the source of truth the
// account-global image optimizer reads — which is what lets that optimizer hold
// no R2 credentials at all.
func TestUploadStaticAssets_MirrorsIdenticalKeysAndBytesToBothTargets(t *testing.T) {
	store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
	cfg := mirrorConfig(imageConfigTree(t), store, asset)

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	key := "assets/proj/web/WEB1/logo.png"
	if got := sortedPuts(store); !reflect.DeepEqual(got, []string{key}) {
		t.Errorf("cache store keys = %v, want %v", got, []string{key})
	}
	want := []string{key, "image-config/proj/web/WEB1.json"}
	if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
		t.Errorf("asset bucket keys = %v, want %v", got, want)
	}
	if store.putBodies[key] != asset.putBodies[key] {
		t.Errorf("mirrored bodies differ: store = %q, asset = %q", store.putBodies[key], asset.putBodies[key])
	}
	if store.contentTypes[key] != asset.contentTypes[key] {
		t.Errorf("mirrored content-types differ: store = %q, asset = %q", store.contentTypes[key], asset.contentTypes[key])
	}
	for _, b := range asset.buckets {
		if b != "assets" {
			t.Errorf("mirrored into bucket %q, want the account's own %q", b, "assets")
		}
	}
}

// TestUploadStaticAssets_PublishesTheImageConfigOutsideThePublicWebRoot pins the
// key the optimizer loads the compiled image config from, and the bytes it
// hashes against the manifest's configHash. The key sits outside
// assets/<project>/<app>/<build id>, which is the app's public web root: a
// config under it would be served to anyone who asked for /image-config.json.
// It goes to the account's own bucket alone — the worker reads the compiled
// patterns off the routing manifest, so an R2 copy would have no reader.
func TestUploadStaticAssets_PublishesTheImageConfigOutsideThePublicWebRoot(t *testing.T) {
	store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
	cfg := mirrorConfig(imageConfigTree(t), store, asset)

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	key := "image-config/proj/web/WEB1.json"
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
		if strings.HasPrefix(k, "image-config/") {
			t.Errorf("cache store received %q, want no image config in R2 at all", k)
		}
	}
}

// TestUploadStaticAssets_AProjectsOwnImageConfigAssetDoesNotCollide proves a
// project shipping public/image-config.json keeps it: its static asset and the
// compiled config are two distinct keys, so neither upload can overwrite the
// other.
func TestUploadStaticAssets_AProjectsOwnImageConfigAssetDoesNotCollide(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":    `{"buildId":"WEB1"}`,
		"apps/web/image-config.json":        `{"formats":["image/webp"]}`,
		"apps/web/static/image-config.json": `{"mine":true}`,
	})
	store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}

	if err := uploadStaticAssets(context.Background(), mirrorConfig(root, store, asset), nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	if got, want := asset.putBodies["assets/proj/web/WEB1/image-config.json"], `{"mine":true}`; got != want {
		t.Errorf("the project's own public/image-config.json = %q, want %q", got, want)
	}
	if got, want := asset.putBodies["image-config/proj/web/WEB1.json"], `{"formats":["image/webp"]}`; got != want {
		t.Errorf("compiled image config = %q, want %q", got, want)
	}
}

// TestUploadStaticAssets_AppWithoutAnImageConfigPublishesNone proves an app that
// generates no /_next/image URLs (a custom loader, or unoptimized images) emits
// no config artifact and the upload treats its absence as normal.
func TestUploadStaticAssets_AppWithoutAnImageConfigPublishesNone(t *testing.T) {
	store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
	cfg := mirrorConfig(staticAppTree(t), store, asset)

	if err := uploadStaticAssets(context.Background(), cfg, twoAppManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	for _, key := range append(sortedPuts(store), sortedPuts(asset)...) {
		if strings.HasPrefix(key, "image-config/") {
			t.Errorf("published %q for a build that emitted no image config", key)
		}
	}
}

// TestUploadStaticAssets_RepublishesTheImageConfigOverAPresentObject proves the
// config is put unconditionally rather than skip-if-exists. Its bytes are what
// the manifest's configHash covers, so a stale object left at this key by an
// earlier publish of the same build id would fail the origin's hash check on
// every image request — whereas re-uploading a static asset that is already
// there would only be waste.
func TestUploadStaticAssets_RepublishesTheImageConfigOverAPresentObject(t *testing.T) {
	present := map[string]bool{
		"image-config/proj/web/WEB1.json": true,
		"assets/proj/web/WEB1/logo.png":   true,
	}
	store := &fakeUploader{exists: present}
	asset := &fakeUploader{exists: present}
	cfg := mirrorConfig(imageConfigTree(t), store, asset)

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	if got := sortedPuts(store); got != nil {
		t.Errorf("cache store keys = %v, want nothing re-put", got)
	}
	want := []string{"image-config/proj/web/WEB1.json"}
	if got := sortedPuts(asset); !reflect.DeepEqual(got, want) {
		t.Errorf("asset bucket keys = %v, want %v", got, want)
	}
}

// TestUploadStaticAssets_EitherTargetFailingFailsTheDeploy proves neither half
// of the asset plane may degrade silently: a build whose assets reached R2 but
// not S3 would serve pages while every image 502s, and one that reached S3 but
// not R2 would 404 its own static files.
func TestUploadStaticAssets_EitherTargetFailingFailsTheDeploy(t *testing.T) {
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

			err := uploadStaticAssets(context.Background(), cfg, nextManifest())
			if err == nil {
				t.Fatal("uploadStaticAssets = nil, want the failed target to fail the deploy")
			}
			if !errors.Is(err, boom) {
				t.Errorf("err = %v, want it to carry what the bucket said", err)
			}
		})
	}
}

// TestUploadStaticAssets_MissingAssetBucketFailsTheDeploy proves a bootstrap
// predating the asset bucket fails loudly rather than publishing the R2 half
// alone: the image optimizer reads only the S3 copy.
func TestUploadStaticAssets_MissingAssetBucketFailsTheDeploy(t *testing.T) {
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: imageConfigTree(t), Env: "prod",
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}

	if err := uploadStaticAssets(context.Background(), cfg, nextManifest()); err == nil {
		t.Fatal("uploadStaticAssets = nil, want an error for a missing asset bucket")
	}
}

// TestUploadPrerenderAssets_RouteEntriesAreNotMirroredToTheAssetBucket proves
// the ISR cache stays single-homed while the asset plane is mirrored: the image
// optimizer never reads a cache entry, so a second copy would double the upload
// time and the storage for no reader. (Fetch entries land in the asset bucket
// for their own, unrelated reason — they are origin-private.)
func TestUploadPrerenderAssets_RouteEntriesAreNotMirroredToTheAssetBucket(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":     `{"buildId":"WEB1"}`,
		"apps/web/cache/index.cache.json":    `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/web/fetch-cache/a1.cache.json": `{"lastModified":2,"value":{"kind":"FETCH"}}`,
	})
	store, asset := &fakeUploader{exists: map[string]bool{}}, &fakeUploader{exists: map[string]bool{}}
	cfg := adoptISRWriter(t, mirrorConfig(root, store, asset))

	if err := uploadPrerenderAssets(context.Background(), cfg, nextManifest()); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	entry := "prod/proj/web/WEB1/cache/index.cache.json"
	if got := sortedPuts(store); !reflect.DeepEqual(got, []string{entry, "prod/proj/web/WEB1/tag-clock.json"}) {
		t.Errorf("cache store keys = %v, want the route entry and the tag clock", got)
	}
	for _, key := range sortedPuts(asset) {
		if key == entry {
			t.Errorf("route entry %q was mirrored to the asset bucket, want it R2-only", key)
		}
	}
}

// TestBuildDeploymentRecord_AssetPrefixIsTheFullR2KeyRoot proves the record
// carries the same prefix uploadStaticAssets published under, so the frozen
// worker needs no project/app identity of its own to read an asset back.
func TestBuildDeploymentRecord_AssetPrefixIsTheFullR2KeyRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root}
	manifest := &deploymentsv1.Manifest{Slug: "proj"}
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if want := "assets/proj/web/WEB1"; record.AssetPrefix != want {
		t.Errorf("AssetPrefix = %q, want %q", record.AssetPrefix, want)
	}
}

// TestBuildDeploymentRecord_IsrPrefixIsTheIsrKeyRoot proves the record carries
// the ISR cache's own key root (isrConfig.Prefix) — the <env>/<project>/<app>/
// <build> root the frozen worker joins its cache-entry and tag-snapshot reads
// onto — and not the DynamoDB tag namespace, which addresses nothing in R2.
func TestBuildDeploymentRecord_IsrPrefixIsTheIsrKeyRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root, Env: "prod"}
	manifest := nextManifest()
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if want := "prod/proj/web/WEB1"; record.IsrPrefix != want {
		t.Errorf("IsrPrefix = %q, want %q", record.IsrPrefix, want)
	}
}

// TestBuildDeploymentRecord_NonNextAppHasNoAssetPrefix proves a non-Next
// app's record carries no AssetPrefix: uploadStaticAssets never publishes
// static output for anything but a Next app, so a prefix here would point at
// a location nothing was ever uploaded to.
func TestBuildDeploymentRecord_NonNextAppHasNoAssetPrefix(t *testing.T) {
	cfg := Config{ArtifactRoot: t.TempDir()}
	manifest := &deploymentsv1.Manifest{Slug: "proj"}
	app := &deploymentsv1.ManifestApp{Name: "api", Framework: "express"}

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("API1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.AssetPrefix != "" {
		t.Errorf("AssetPrefix = %q, want empty for a non-Next app", record.AssetPrefix)
	}
}

// TestBuildDeploymentRecord_CarriesTheBuildsWriteSecret proves the record hands
// the frozen edge worker the secret its tag raises authenticate with. The
// secret is per-build and the worker script outlives every build it serves, so
// the Deployment record is the only place it can ride.
func TestBuildDeploymentRecord_CarriesTheBuildsWriteSecret(t *testing.T) {
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

	record, err := buildDeploymentRecord(cfg, manifest, app, buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if want := isrWriteSecret("seed-1", "prod/proj/web/WEB1"); record.IsrWriteSecret != want {
		t.Errorf("IsrWriteSecret = %q, want the secret derived for this build's prefix", record.IsrWriteSecret)
	}
}

// A substrate that adopted no writer has no secret to derive, so the record
// carries none and the edge raises nowhere rather than with a made-up token.
func TestBuildDeploymentRecord_NoWriterLeavesNoWriteSecret(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})
	cfg := Config{ArtifactRoot: root, Env: "prod"}
	record, err := buildDeploymentRecord(cfg, nextManifest(), &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}, buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.IsrWriteSecret != "" {
		t.Errorf("IsrWriteSecret = %q, want empty", record.IsrWriteSecret)
	}
}
