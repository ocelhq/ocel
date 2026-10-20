package site

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

func edgeAppTree(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"apps/web/next-route-table.json":   `{"buildId":"WEB1"}`,
		"apps/web/edge/bundle.json":        `{"version":1,"mainModule":"main.js"}`,
		"apps/admin/next-route-table.json": `{"buildId":"ADM1"}`,
	})
}

func edgeBundleKeyFor(app, buildID string) string {
	return kindKeyFor("bundles", app, buildID, "bundle.json")
}

func TestAppEdgeBundleKey(t *testing.T) {
	coord := storageCoordinate("prod", "proj", "web", deployedAs(testBuildID).Token())
	got := appEdgeBundleKey(coord)
	want := edgeBundleKeyFor("web", testBuildID)
	if got != want {
		t.Errorf("appEdgeBundleKey = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, coord.StoragePrefix(naming.ObjectBundles)) {
		t.Errorf("key %q must live under the release's own prune-able prefix", got)
	}
	if other := appEdgeBundleKey(storageCoordinate("prod", "proj", "web", deployedAs("d2").Token())); other == got {
		t.Error("two releases of one app must not share a bundle key")
	}
}

func TestUploadEdgeBundles(t *testing.T) {
	t.Run("uploads each bundle under its own build prefix", func(t *testing.T) {
		t.Parallel()
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: edgeAppTree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: store, EdgeHoldsKey: true}}

		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}

		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{edgeBundleKeyFor("web", testBuildID)}
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
			if b != "edge" {
				t.Errorf("uploaded into bucket %q, want the edge bucket %q", b, "edge")
			}
		}
	})

	t.Run("replaces the object already under the key", func(t *testing.T) {
		t.Parallel()
		key := edgeBundleKeyFor("web", testBuildID)
		store := &fakeArtifactStore{exists: map[string]bool{key: true}}
		cfg := publication{root: writeTree(t, map[string]string{
			"apps/web/next-route-table.json": `{"buildId":"WEB1"}`,
			"apps/web/edge/bundle.json":      `{"version":1,"mainModule":"main.js","shim":"REBUILT"}`,
		}), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: store, EdgeHoldsKey: true}}

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
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: edgeAppTree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: store, EdgeHoldsKey: true}}

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
		if len(store.puts) != 2 || len(distinct) != 1 || !distinct[edgeBundleKeyFor("web", testBuildID)] {
			t.Fatalf("uploaded keys = %v, want the same %q twice", store.puts, edgeBundleKeyFor("web", testBuildID))
		}
		if body := store.putBodies[edgeBundleKeyFor("web", testBuildID)]; body != `{"version":1,"mainModule":"main.js"}` {
			t.Errorf("object after the rotation = %q, want the build's bundle unchanged", body)
		}
	})

	t.Run("a build with no edge bundle uploads nothing", func(t *testing.T) {
		t.Parallel()
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: nodeAppTree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: store, EdgeHoldsKey: true}}

		if err := uploadEdgeBundles(context.Background(), cfg, nodeManifest(), appBuildsFor(t, cfg, nodeManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		if len(store.puts) != 0 {
			t.Errorf("store received %v, want nothing from a build that emitted no bundle", store.puts)
		}
	})

	t.Run("unadopted store uploads nothing", func(t *testing.T) {
		t.Parallel()
		asset := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := publication{root: edgeAppTree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: asset}}

		if err := uploadEdgeBundles(context.Background(), cfg, twoAppManifest(), appBuildsFor(t, cfg, twoAppManifest())); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		if len(asset.puts) != 0 {
			t.Errorf("edge bucket received %v, want nothing with no adopted store", asset.puts)
		}
	})
}

func edgeVariablesManifest(variables ...*contractv1.ManifestVariable) *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "proj",
		Apps: []*contractv1.ManifestApp{
			{Name: "web", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}, Folder: "/shop", Variables: variables,
				Artifact: serverlessArtifact(&contractv1.ManifestFunction{LogicalName: "web_index", Framework: &contractv1.Framework{Name: "next"}})},
		},
	}
}

func edgeBuilds(t *testing.T, cfg publication, manifest *contractv1.Manifest) appBuilds {
	t.Helper()
	bundles := map[string]variables.Bundle{}
	for _, app := range manifest.GetApps() {
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
		bundle, err := variables.Seal(liveVariables, manifest.GetSlug(), app.GetName(), sensitive, keys, nil)
		if err != nil {
			t.Fatalf("sealAppBundle(%s): %v", app.GetName(), err)
		}
		bundles[app.GetName()] = bundle
	}
	builds := appBuildsFor(t, cfg, manifest)
	builds.baked = bundles
	return builds
}

func edgeSealedKeyFor(app, buildID string) string {
	return kindKeyFor("bundles", app, buildID, "sealed.bin")
}

func edgeStoreConfig(t *testing.T, store *fakeArtifactStore) publication {
	t.Helper()
	return publication{root: edgeAppTree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: store, EdgeHoldsKey: true}}
}

func TestUploadEdgeSeal(t *testing.T) {
	t.Run("the sealed overlay rides beside the bundle", func(t *testing.T) {
		t.Parallel()
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeStoreConfig(t, store)
		manifest := edgeVariablesManifest(
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
		)
		builds := edgeBuilds(t, cfg, manifest)

		if err := uploadEdgeBundles(context.Background(), cfg, manifest, builds); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}

		got := append([]string(nil), store.puts...)
		slices.Sort(got)
		want := []string{edgeBundleKeyFor("web", testBuildID), edgeSealedKeyFor("web", testBuildID)}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("uploaded keys = %v, want %v", got, want)
		}
		sealed := edgeSealedKeyFor("web", testBuildID)
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
		if body := store.putBodies[edgeBundleKeyFor("web", testBuildID)]; body != `{"version":1,"mainModule":"main.js"}` {
			t.Errorf("bundle body = %q, want the automation's output verbatim", body)
		}
	})

	t.Run("plain declarations alone seal nothing", func(t *testing.T) {
		t.Parallel()
		store := &fakeArtifactStore{exists: map[string]bool{}}
		cfg := edgeStoreConfig(t, store)
		manifest := edgeVariablesManifest(variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		if err := uploadEdgeBundles(context.Background(), cfg, manifest, edgeBuilds(t, cfg, manifest)); err != nil {
			t.Fatalf("uploadEdgeBundles: %v", err)
		}
		want := edgeBundleKeyFor("web", testBuildID)
		if len(store.puts) != 1 || store.puts[0] != want {
			t.Fatalf("uploaded keys = %v, want the bundle alone at %q", store.puts, want)
		}
	})

}

func TestAnEdgeBundleAndItsSealedOverlayLandInTheEdgeBucketUntagged(t *testing.T) {
	t.Parallel()
	assetStore := &fakeArtifactStore{exists: map[string]bool{}}
	cfg := publication{root: writeTree(t, map[string]string{
		"apps/web/next-route-table.json": `{"buildId":"WEB1"}`,
		"apps/web/edge/bundle.json":      `{"version":1,"mainModule":"main.js"}`,
	}), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: assetStore, EdgeHoldsKey: true}}
	manifest := edgeVariablesManifest(variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE))

	if err := uploadEdgeBundles(context.Background(), cfg, manifest, edgeBuilds(t, cfg, manifest)); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}

	want := []string{edgeBundleKeyFor("web", testBuildID), edgeSealedKeyFor("web", testBuildID)}
	slices.Sort(want)
	if got := sortedPuts(assetStore); !slices.Equal(got, want) {
		t.Errorf("the edge bucket was given %v, want %v", got, want)
	}
	if len(assetStore.taggings) != 0 {
		t.Errorf("taggings = %v, want none", assetStore.taggings)
	}
	for _, b := range assetStore.buckets {
		if b != "edge" {
			t.Errorf("the edge-bucket copy went to bucket %q, want %q", b, "edge")
		}
	}
}

func appValuesOf(app *contractv1.ManifestApp) provider.AppValues {
	plain, sensitive := map[string]string{}, map[string]string{}
	for _, v := range app.GetVariables() {
		switch v.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			plain[v.GetKey()] = v.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			sensitive[v.GetKey()] = v.GetValue()
		}
	}
	return provider.AppValues{Plain: plain, Sensitive: sensitive, Folder: app.GetFolder()}
}

func TestCheckAppEdgeVariables(t *testing.T) {
	t.Run("an over-budget edge environment fails the deploy with no edge key", func(t *testing.T) {
		t.Parallel()
		cfg := publication{root: edgeAppTree(t)}
		manifest := edgeVariablesManifest(variable("BIG_ONE", strings.Repeat("a", variables.FunctionEnvBudgetBytes), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		app := manifest.GetApps()[0]

		err := CheckEdgeVariables(cfg.root, app.GetName(), appValuesOf(app), edgeBuilds(t, cfg, manifest).baked[app.GetName()])
		if err == nil {
			t.Fatal("CheckEdgeVariables = nil, want the deploy refused")
		}
		if !strings.Contains(err.Error(), "web") {
			t.Errorf("error = %q, want it to name the app", err)
		}
	})

	t.Run("an app with no edge output is left alone", func(t *testing.T) {
		t.Parallel()
		cfg := publication{root: t.TempDir()}
		manifest := edgeVariablesManifest(variable("BIG_ONE", strings.Repeat("a", variables.FunctionEnvBudgetBytes), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		app := manifest.GetApps()[0]
		if err := CheckEdgeVariables(cfg.root, app.GetName(), appValuesOf(app), variables.Bundle{}); err != nil {
			t.Errorf("CheckEdgeVariables = %v, want an app that ships no edge worker accepted", err)
		}
	})
}
