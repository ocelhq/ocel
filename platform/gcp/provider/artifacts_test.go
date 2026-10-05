package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestACacheObjectIsNamedForItsProjectThenAppThenEnvironment(t *testing.T) {
	for key, want := range map[string]string{
		"prod/shop/web/r1a2b3c4d/isr/cache/index.cache.json": "cache/shop/web/prod/r1a2b3c4d/isr/cache/index.cache.json",
		"prod/shop/web/r1/isr/":                              "cache/shop/web/prod/r1/isr/",
		"conformance/TestX/bundle.zip":                       "cache/TestX/bundle.zip/conformance",
		"a/b":                                                "cache/a/b",
	} {
		got, err := objectName(provider.ArtifactRef{Bucket: provider.StoreCache, Key: key})
		if err != nil {
			t.Fatalf("objectName(%q) = %v", key, err)
		}
		if got != want {
			t.Errorf("a cache artifact keyed %q is named %q, want %q", key, got, want)
		}
	}
}

func TestAFunctionsOrAssetsObjectKeepsItsKeyAsItsName(t *testing.T) {
	for store, want := range map[string]string{
		provider.StoreFunctions: "functions/prod/shop/web/r1/x",
		provider.StoreAssets:    "assets/prod/shop/web/r1/x",
	} {
		got, err := objectName(provider.ArtifactRef{Bucket: store, Key: "prod/shop/web/r1/x"})
		if err != nil {
			t.Fatalf("objectName(%s) = %v", store, err)
		}
		if got != want {
			t.Errorf("a %s artifact is named %q, want %q", store, got, want)
		}
	}
}

func TestAReclaimedReleasePrefixSweepsExactlyItsCacheObjects(t *testing.T) {
	for prefix, want := range map[string]string{
		"prod/shop/web/r1/":     "cache/shop/web/prod/r1/",
		"prod/shop/web/r1/isr/": "cache/shop/web/prod/r1/isr/",
	} {
		list, keeps, err := cacheSweep(prefix)
		if err != nil {
			t.Fatalf("cacheSweep(%q) = %v", prefix, err)
		}
		if list != want {
			t.Errorf("sweeping %q lists %q, want %q", prefix, list, want)
		}
		if !keeps("cache/shop/web/prod/r1/anything") || !keeps("cache/other") {
			t.Errorf("sweeping %q keeps a listed name, want every listed name deleted", prefix)
		}
	}
}

func TestRemovingAProjectsEnvironmentFromTheCacheListsOnlyThatProject(t *testing.T) {
	list, keeps, err := cacheSweep("pr-7/shop/")
	if err != nil {
		t.Fatal(err)
	}
	if list != "cache/shop/" {
		t.Errorf("removing pr-7/shop/ lists %q, want cache/shop/", list)
	}
	if !keeps("cache/shop/web/pr-7/r1/isr/tag-clock.json") {
		t.Error("removing pr-7/shop/ leaves pr-7's own object, want it deleted")
	}
	for _, name := range []string{"cache/shop/web/prod/r1/isr/tag-clock.json", "cache/shop/web"} {
		if keeps(name) {
			t.Errorf("removing pr-7/shop/ deletes %q, want it left", name)
		}
	}
}

func TestACachePrefixNamingNoWholeProjectAndEnvironmentIsRefused(t *testing.T) {
	for _, prefix := range []string{"prod/", "prod", "prod/shop"} {
		_, _, err := cacheSweep(prefix)
		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
			t.Errorf("cacheSweep(%q) = %v, want an invalid-input refusal", prefix, err)
		}
	}
}
