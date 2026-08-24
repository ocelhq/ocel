package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

func edgeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":   `{"buildId":"WEB1"}`,
		"apps/web/edge/bundle.json":        `{"version":1,"mainModule":"main.js"}`,
		"apps/admin/routing-manifest.json": `{"buildId":"ADM1"}`,
	})
}

func edgeBundleKeyFor(app, deploymentID string) string {
	return storagePrefixFor("prod", "proj", app, deploymentID) + "edge/bundle.json"
}

func TestAppEdgeBundleKey(t *testing.T) {
	coord := storageCoordinate("prod", "proj", "web", releaseOf(deployedAs(testDeploymentID)))
	got := appEdgeBundleKey(coord)
	want := edgeBundleKeyFor("web", testDeploymentID)
	if got != want {
		t.Errorf("appEdgeBundleKey = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, appEdgePrefix(coord)+"/") {
		t.Errorf("key %q must live under the release's own prune-able prefix", got)
	}
	if other := appEdgeBundleKey(storageCoordinate("prod", "proj", "web", releaseOf(deployedAs("d2")))); other == got {
		t.Error("two releases of one app must not share a bundle key")
	}
}

func TestUploadEdgeBundles(t *testing.T) {
	t.Run("uploads each bundle under its own build prefix", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}

		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{edgeBundleKeyFor("web", testDeploymentID)}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		if ct := store.contentTypes[want[0]]; ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if body := store.putBodies[want[0]]; body != `{"version":1,"mainModule":"main.js"}` {
			t.Errorf("uploaded body = %q, want the bundle verbatim", body)
		}
		for _, b := range store.buckets {
			if b != "isr" {
				t.Errorf("uploaded into bucket %q, want the adopted store %q", b, "isr")
			}
		}
	})

	t.Run("replaces the object already under the key", func(t *testing.T) {
		t.Parallel()
		key := edgeBundleKeyFor("web", testDeploymentID)
		store := &fakeUploader{exists: map[string]bool{key: true}}
		cfg := Config{
			ArtifactRoot: writeTree(t, map[string]string{
				"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
				"apps/web/edge/bundle.json":      `{"version":1,"mainModule":"main.js","shim":"REBUILT"}`,
			}),
			AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadEdgeBundles(context.Background(), cfg, nextManifest(), appBuildsFor(t, cfg, nextManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}

		if len(store.puts) != 1 || store.puts[0] != key {
			t.Fatalf("uploaded keys = %v, want the bundle rewritten at %q", store.puts, key)
		}
		if !strings.Contains(store.putBodies[key], "REBUILT") {
			t.Errorf("object at %q = %q, want this build's bundle", key, store.putBodies[key])
		}
	})

	t.Run("a rotation targets the same build key", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("first uploadEdgeBundles: %v", err)
		}
		for _, key := range store.puts {
			store.exists[key] = true
		}
		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("rotation uploadEdgeBundles: %v", err)
		}

		distinct := map[string]bool{}
		for _, key := range store.puts {
			distinct[key] = true
		}
		if len(store.puts) != 2 || len(distinct) != 1 || !distinct[edgeBundleKeyFor("web", testDeploymentID)] {
			t.Fatalf("uploaded keys = %v, want the same %q twice", store.puts, edgeBundleKeyFor("web", testDeploymentID))
		}
		if body := store.putBodies[edgeBundleKeyFor("web", testDeploymentID)]; body != `{"version":1,"mainModule":"main.js"}` {
			t.Errorf("object after the rotation = %q, want the build's bundle unchanged", body)
		}
	})

	t.Run("a build with no edge bundle uploads nothing", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{
			ArtifactRoot: nodeAppTree(t), AssetBucket: "assets", Env: "prod",
			Uploader:         &fakeUploader{exists: map[string]bool{}},
			CacheStoreBucket: "isr", CacheStoreUploader: store,
		}

		if err := uploadEdgeBundles(context.Background(), cfg, nodeManifest(), appBuildsFor(t, cfg, nodeManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		if len(store.puts) != 0 {
			t.Errorf("store received %v, want nothing from a build that emitted no bundle", store.puts)
		}
	})

	t.Run("unadopted store uploads nothing", func(t *testing.T) {
		t.Parallel()
		asset := &fakeUploader{exists: map[string]bool{}}
		cfg := Config{ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod", Uploader: asset}

		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		if len(asset.puts) != 0 {
			t.Errorf("asset bucket received %v, want nothing with no adopted store", asset.puts)
		}
	})
}

func edgeVarsManifest(variables ...*contractv1.ManifestVariable) *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: frameworkNext, Folder: "/shop", Variables: variables},
		},
		Functions: []*contractv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
		},
	}
}

func edgeBuilds(t *testing.T, cfg Config, manifest *contractv1.Manifest) appBuilds {
	t.Helper()
	bundles := map[string]appBundle{}
	for _, app := range manifestApps(manifest) {
		sensitive := map[string]string{}
		var keys []live.Key
		for _, v := range app.GetVariables() {
			switch v.GetClass() {
			case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
				sensitive[v.GetKey()] = v.GetValue()
			case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
				keys = append(keys, live.Key{Key: v.GetKey(), Folder: v.GetFolder()})
			}
		}
		bundle, err := sealAppBundle(liveConfig(), manifest.GetSlug(), app.GetName(), sensitive, keys, nil)
		if err != nil {
			t.Fatalf("sealAppBundle(%s): %v", app.GetName(), err)
		}
		bundles[app.GetName()] = bundle
	}
	builds := appBuildsFor(t, cfg, manifest)
	builds.baked = bundles
	return builds
}

func edgeSealedKeyFor(app, deploymentID string) string {
	return storagePrefixFor("prod", "proj", app, deploymentID) + "edge/sealed.bin"
}

func edgeStoreConfig(t *testing.T, store *fakeUploader) Config {
	t.Helper()
	return Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
	}
}

func TestUploadEdgeSeal(t *testing.T) {
	t.Run("the sealed overlay rides beside the bundle", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := edgeStoreConfig(t, store)
		manifest := edgeVarsManifest(
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
		)
		builds := edgeBuilds(t, cfg, manifest)

		if err := uploadEdgeBundles(context.Background(), cfg, manifest, builds); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}

		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{edgeBundleKeyFor("web", testDeploymentID), edgeSealedKeyFor("web", testDeploymentID)}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		sealed := edgeSealedKeyFor("web", testDeploymentID)
		if ct := store.contentTypes[sealed]; ct != "application/octet-stream" {
			t.Errorf("content-type = %q, want application/octet-stream", ct)
		}
		if body := store.putBodies[sealed]; body != string(builds.baked["web"].Ciphertext) {
			t.Error("the sealed object is not the ciphertext the function already ships")
		}
		for key, body := range store.putBodies {
			if strings.Contains(body, "sk-live") {
				t.Errorf("object at %q discloses a sensitive value", key)
			}
		}
		if body := store.putBodies[edgeBundleKeyFor("web", testDeploymentID)]; body != `{"version":1,"mainModule":"main.js"}` {
			t.Errorf("bundle body = %q, want the adapter's output verbatim", body)
		}
	})

	t.Run("plain declarations alone seal nothing", func(t *testing.T) {
		t.Parallel()
		store := &fakeUploader{exists: map[string]bool{}}
		cfg := edgeStoreConfig(t, store)
		manifest := edgeVarsManifest(variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if err := uploadEdgeBundles(context.Background(), cfg, manifest, edgeBuilds(t, cfg, manifest)); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		want := edgeBundleKeyFor("web", testDeploymentID)
		if len(store.puts) != 1 || store.puts[0] != want {
			t.Fatalf("uploaded keys = %v, want the bundle alone at %q", store.puts, want)
		}
	})

}

func appValuesOf(app *contractv1.ManifestApp) providerkit.AppValues {
	plain := map[string]string{}
	for _, v := range app.GetVariables() {
		if v.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			plain[v.GetKey()] = v.GetValue()
		}
	}
	return providerkit.AppValues{Plain: plain, Folder: app.GetFolder()}
}

func TestCheckAppEdgeVariables(t *testing.T) {
	t.Run("a name the entry worker owns fails the deploy with no cache store", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod"}
		manifest := edgeVarsManifest(variable("OCEL_CACHE_SCOPE", "mine", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		app := manifest.GetApps()[0]

		err := checkAppEdgeVariables(cfg, app.GetName(), appValuesOf(app), edgeBuilds(t, cfg, manifest).baked[app.GetName()])
		if err == nil {
			t.Fatal("checkAppEdgeVariables = nil, want the deploy refused")
		}
		if !strings.Contains(err.Error(), "OCEL_CACHE_SCOPE") {
			t.Errorf("error = %q, want it to name the variable", err)
		}
	})

	t.Run("an over-budget edge environment fails the deploy with no cache store", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod"}
		manifest := edgeVarsManifest(variable("BIG_ONE", strings.Repeat("a", functionEnvBudgetBytes), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		app := manifest.GetApps()[0]

		err := checkAppEdgeVariables(cfg, app.GetName(), appValuesOf(app), edgeBuilds(t, cfg, manifest).baked[app.GetName()])
		if err == nil {
			t.Fatal("checkAppEdgeVariables = nil, want the deploy refused")
		}
		if !strings.Contains(err.Error(), "web") {
			t.Errorf("error = %q, want it to name the app", err)
		}
	})

	t.Run("an app with no edge output is left alone", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: t.TempDir(), Env: "prod"}
		manifest := edgeVarsManifest(variable("OCEL_CACHE_SCOPE", "mine", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		app := manifest.GetApps()[0]
		if err := checkAppEdgeVariables(cfg, app.GetName(), appValuesOf(app), appBundle{}); err != nil {
			t.Errorf("checkAppEdgeVariables = %v, want an app that ships no edge worker accepted", err)
		}
	})
}

func TestLoaderID(t *testing.T) {
	t.Run("covers the runtime not just the bundle", func(t *testing.T) {
		t.Parallel()
		bundle := []byte(`{"version":1}`)
		base := loaderID(bundle, "2026-07-13", []string{"nodejs_compat"})

		if again := loaderID(bundle, "2026-07-13", []string{"nodejs_compat"}); again != base {
			t.Error("id changed with nothing changed; a redeploy must reuse the loaded code")
		}
		for _, tc := range []struct {
			name string
			got  string
		}{
			{"changed bundle", loaderID([]byte(`{"version":2}`), "2026-07-13", []string{"nodejs_compat"})},
			{"changed compat date", loaderID(bundle, "2026-09-01", []string{"nodejs_compat"})},
			{"changed compat flag", loaderID(bundle, "2026-07-13", []string{"nodejs_compat", "no_nodejs_compat_v2"})},
			{"dropped compat flag", loaderID(bundle, "2026-07-13", nil)},
		} {
			if tc.got == base {
				t.Errorf("%s: id unchanged, want a new id for code the loader evaluates differently", tc.name)
			}
		}
	})
}
