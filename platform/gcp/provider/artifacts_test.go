package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
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
	if !keeps("cache/shop/web/pr-7/r1/isr/cache/index.cache.json") {
		t.Error("removing pr-7/shop/ leaves pr-7's own object, want it deleted")
	}
	for _, name := range []string{"cache/shop/web/prod/r1/isr/cache/index.cache.json", "cache/shop/web"} {
		if keeps(name) {
			t.Errorf("removing pr-7/shop/ deletes %q, want it left", name)
		}
	}
}

func TestACachePrefixNamingNoWholeProjectAndEnvironmentIsRefused(t *testing.T) {
	for _, prefix := range []string{"prod/", "prod", "prod/shop", "prod/shop/web/r1", "prod/shop/web/r1/isr"} {
		_, _, err := cacheSweep(prefix)
		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
			t.Errorf("cacheSweep(%q) = %v, want an invalid-input refusal", prefix, err)
		}
	}
}

func TestACacheAppPrefixIsTheProjectThenTheAppOfTheKey(t *testing.T) {
	got, err := cacheAppPrefix("prod/shop/web/r1/isr")
	if err != nil {
		t.Fatalf("cacheAppPrefix() = %v", err)
	}
	if want := "cache/shop/web/"; got != want {
		t.Errorf("cacheAppPrefix(prod/shop/web/r1/isr) = %q, want %q", got, want)
	}
}

func TestACacheAppPrefixOfAKeyNamingNoAppIsRefused(t *testing.T) {
	_, err := cacheAppPrefix("prod/shop")
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("cacheAppPrefix(prod/shop) = %v, want an invalid-input refusal", err)
	}
}

func TestPruningAReleasesISRPrefixPlansItsCacheAndUseCacheEntriesOnly(t *testing.T) {
	list, keeps, err := cacheSweep("prod/shop/web/r1a2b3c4d/isr/")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cache/shop/web/prod/r1a2b3c4d/isr/"; list != want {
		t.Fatalf("pruning the ISR prefix lists %q, want %q", list, want)
	}
	for _, name := range []string{
		"cache/shop/web/prod/r1a2b3c4d/isr/cache/index.cache.json",
		"cache/shop/web/prod/r1a2b3c4d/isr/fetch-cache/h.cache.json",
		"cache/shop/web/prod/r1a2b3c4d/isr/use-cache/abc.json",
	} {
		if !keeps(name) {
			t.Errorf("pruning the ISR prefix leaves %q, want it deleted", name)
		}
	}
	if list2 := "cache/shop/web/prod/r9z8y7x6w/isr/cache/index.cache.json"; strings.HasPrefix(list2, list) {
		t.Errorf("the listing %q reaches another release's cache entry %q", list, list2)
	}
}

func TestDestroyingAReleasePlansEveryCacheObjectOfThatReleaseAndNoOther(t *testing.T) {
	list, keeps, err := cacheSweep("prod/shop/web/r1a2b3c4d/")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cache/shop/web/prod/r1a2b3c4d/"; list != want {
		t.Fatalf("destroying a release lists %q, want %q", list, want)
	}
	for _, other := range []string{
		"cache/shop/web/prod/r9z8y7x6w/isr/cache/index.cache.json",
		"cache/shop/web/prod/r1a2b3c4de/isr/cache/index.cache.json",
	} {
		if strings.HasPrefix(other, list) {
			t.Errorf("the listing %q reaches %q, another release", list, other)
		}
	}
	if !keeps("cache/shop/web/prod/r1a2b3c4d/isr/cache/index.cache.json") {
		t.Error("destroying a release leaves its cache entry, want it deleted")
	}
}

func TestRemovingAPreviewEnvironmentPlansOnlyThatEnvironmentsCacheObjects(t *testing.T) {
	_, keeps, err := cacheSweep("pr-7/shop/")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"cache/shop/web/pr-7/r1/isr/cache/index.cache.json":  true,
		"cache/shop/api/pr-7/r2/isr/x":                       true,
		"cache/shop/web/prod/r1/isr/cache/index.cache.json":  false,
		"cache/shop/web/pr-70/r1/isr/cache/index.cache.json": false,
	} {
		if got := keeps(name); got != want {
			t.Errorf("removing pr-7/shop/ deletes %q = %v, want %v", name, got, want)
		}
	}
}

func TestRemovingAProjectsEnvironmentListsNoOtherProjectsCache(t *testing.T) {
	list, _, err := cacheSweep("pr-7/shop/")
	if err != nil {
		t.Fatal(err)
	}
	if list != "cache/shop/" {
		t.Errorf("removing pr-7/shop/ lists %q, want exactly cache/shop/", list)
	}
	if neighbour := "cache/shopping/web/pr-7/r1/isr/x"; strings.HasPrefix(neighbour, list) {
		t.Errorf("the listing %q reaches %q, another project", list, neighbour)
	}
}

type destroyedPrefixes struct {
	mu    sync.Mutex
	paths []string
	auths []string
}

func servingDestroys(t *testing.T) (*destroyedPrefixes, string) {
	t.Helper()
	seen := &destroyedPrefixes{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		seen.paths = append(seen.paths, r.Method+" "+r.URL.Path)
		seen.auths = append(seen.auths, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return seen, server.URL
}

func adoptingTheWriter(t *testing.T, writerURL string) artifacts {
	t.Helper()
	p := (&runServer{}).open(t)
	adoptBehindTheWorker(t, p, writerURL)
	return artifacts{p}
}

func TestPruningAReleaseBehindCloudflareDestroysItsISRWriterRecord(t *testing.T) {
	t.Parallel()
	seen, url := servingDestroys(t)
	store := adoptingTheWriter(t, url)

	for _, prefix := range []string{"prod/shop/web/r1a2b3c4d/", "prod/shop/web/r1a2b3c4d/isr/"} {
		seen.paths, seen.auths = nil, nil
		if err := store.retireISRWriter(context.Background(), environment.TierProduction, prefix); err != nil {
			t.Fatalf("retireISRWriter(%s) = %v", prefix, err)
		}
		if len(seen.paths) != 1 || seen.paths[0] != "POST /prod/shop/web/r1a2b3c4d/isr/destroy" || seen.auths[0] != "Bearer c2" {
			t.Errorf("retireISRWriter(%s) called %v with %v, want one destroy of the release's isr prefix under the writer's bootstrap credential", prefix, seen.paths, seen.auths)
		}
	}
}

func TestPruningAReleaseWithNoAdoptedISRWriterCallsNoWriter(t *testing.T) {
	t.Parallel()
	seen, _ := servingDestroys(t)
	p := (&runServer{}).open(t)
	p.records = newOffersHarness(t).records

	if err := (artifacts{p}).retireISRWriter(context.Background(), environment.TierProduction, "prod/shop/web/r1a2b3c4d/"); err != nil {
		t.Fatalf("retireISRWriter() = %v, want nothing to do where no edge adopted a writer", err)
	}
	if len(seen.paths) != 0 {
		t.Errorf("called %v, want no call", seen.paths)
	}
}

func TestRemovingAProjectPrefixOnGCPDestroysNoISRWriterRecord(t *testing.T) {
	t.Parallel()
	seen, url := servingDestroys(t)
	store := adoptingTheWriter(t, url)

	for _, prefix := range []string{"prod/shop/", "prod/", "prod/shop/web/"} {
		if err := store.retireISRWriter(context.Background(), environment.TierProduction, prefix); err != nil {
			t.Fatalf("retireISRWriter(%s) = %v", prefix, err)
		}
	}
	if len(seen.paths) != 0 {
		t.Errorf("called %v for prefixes that name no release", seen.paths)
	}
}
