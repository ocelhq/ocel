package naming_test

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type fixtureCoordinate struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	App     string `json:"app"`
	Release string `json:"release"`
}

type storageKeysFixture struct {
	Keys []struct {
		Key string `json:"key"`
		fixtureCoordinate
		Kind string `json:"kind"`
		Rest string `json:"rest"`
	} `json:"keys"`
	Prefixes []struct {
		Kind       string            `json:"kind"`
		Coordinate fixtureCoordinate `json:"coordinate"`
		Prefix     string            `json:"prefix"`
	} `json:"prefixes"`
	Coordinates []struct {
		Value      string            `json:"value"`
		Coordinate fixtureCoordinate `json:"coordinate"`
	} `json:"coordinates"`
	InvalidCoordinates []string `json:"invalidCoordinates"`
	Invalid            []string `json:"invalid"`
}

func readStorageKeys(t *testing.T) storageKeysFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "storage-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture storageKeysFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func coordinateOf(t *testing.T, c fixtureCoordinate) naming.Coordinate {
	t.Helper()
	coordinate := naming.Coordinate{Project: c.Project, Env: c.Env, App: c.App}
	if c.Release != "" {
		release, err := naming.ParseReleaseToken(c.Release)
		if err != nil {
			t.Fatal(err)
		}
		coordinate.Release = release
	}
	return coordinate
}

func TestAnObjectKeyParsesToTheKindAndCoordinateTheEdgeContractReadsFromIt(t *testing.T) {
	for _, want := range readStorageKeys(t).Keys {
		got, err := naming.ParseObjectKey(want.Key)
		if err != nil {
			t.Errorf("ParseObjectKey(%q) = %v", want.Key, err)
			continue
		}
		if got.Kind != naming.ObjectKind(want.Kind) || got.Coordinate != coordinateOf(t, want.fixtureCoordinate) || got.Rest != want.Rest {
			t.Errorf("ParseObjectKey(%q) = %+v, want %+v", want.Key, got, want)
		}
	}
}

func TestAnObjectKeyIsTheKindThenTheCoordinateThenTheRest(t *testing.T) {
	for _, want := range readStorageKeys(t).Keys {
		coordinate := coordinateOf(t, want.fixtureCoordinate)
		if got := coordinate.ObjectKey(naming.ObjectKind(want.Kind), want.Rest); got != want.Key {
			t.Errorf("ObjectKey(%s, %q) = %q, want %q", want.Kind, want.Rest, got, want.Key)
		}
	}
}

func TestAReleasesStoragePrefixIsTheKindThenItsCoordinate(t *testing.T) {
	for _, want := range readStorageKeys(t).Prefixes {
		coordinate := coordinateOf(t, want.Coordinate)
		if got := coordinate.StoragePrefix(naming.ObjectKind(want.Kind)); got != want.Prefix {
			t.Errorf("StoragePrefix(%s) of %+v = %q, want %q", want.Kind, want.Coordinate, got, want.Prefix)
		}
	}
}

func TestAStorageScopeNamesTheObjectsOfAProjectAnAppOrARelease(t *testing.T) {
	release := naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Kind: naming.KindFunction, Name: "api", Release: naming.NewReleaseToken("b1", "f1")}
	project, err := naming.ProjectScope("shop")
	if err != nil {
		t.Fatal(err)
	}
	env, err := naming.EnvScope("shop", "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	app, err := naming.AppScope(release)
	if err != nil {
		t.Fatal(err)
	}
	released, err := naming.ReleaseScope(release)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		scope naming.StorageScope
		want  string
	}{
		{project, "use-cache/shop/"},
		{env, "use-cache/shop/pr-7/"},
		{app, "use-cache/shop/pr-7/web/"},
		{released, "use-cache/shop/pr-7/web/" + release.Release.String() + "/"},
	} {
		got, err := c.scope.Prefix(naming.ObjectUseCache)
		if err != nil || got != c.want {
			t.Errorf("Prefix(use-cache) = %q, %v, want %q", got, err, c.want)
		}
		if c.scope.Project() != "shop" {
			t.Errorf("Project() = %q, want shop", c.scope.Project())
		}
	}
}

func TestAStorageScopeWithAMissingOrMalformedFieldIsRefused(t *testing.T) {
	release := naming.NewReleaseToken("b1", "f1")
	for name, build := range map[string]func() (naming.StorageScope, error){
		"an empty project":       func() (naming.StorageScope, error) { return naming.ProjectScope("") },
		"an uppercase project":   func() (naming.StorageScope, error) { return naming.ProjectScope("Shop") },
		"an env with no project": func() (naming.StorageScope, error) { return naming.EnvScope("", "pr-7") },
		"an env with no env":     func() (naming.StorageScope, error) { return naming.EnvScope("shop", "") },
		"an app with no env": func() (naming.StorageScope, error) {
			return naming.AppScope(naming.Coordinate{Project: "shop", App: "web"})
		},
		"an app with no app": func() (naming.StorageScope, error) {
			return naming.AppScope(naming.Coordinate{Project: "shop", Env: "pr-7"})
		},
		"a release with no env": func() (naming.StorageScope, error) {
			return naming.ReleaseScope(naming.Coordinate{Project: "shop", App: "web", Release: release})
		},
		"a release with no token": func() (naming.StorageScope, error) {
			return naming.ReleaseScope(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web"})
		},
		"a release with no project": func() (naming.StorageScope, error) {
			return naming.ReleaseScope(naming.Coordinate{Env: "pr-7", App: "web", Release: release})
		},
	} {
		if scope, err := build(); err == nil {
			t.Errorf("%s: scope = %+v, want an error", name, scope)
		}
	}
}

func TestTheZeroStorageScopeIsRefusedRatherThanNamingEveryProject(t *testing.T) {
	var refused refusal.Refusal
	if got, err := (naming.StorageScope{}).Prefix(naming.ObjectAssets); !errors.As(err, &refused) {
		t.Errorf("Prefix() of the zero scope = %q, %v, want a refusal", got, err)
	}
}

func TestBucketsHoldEachKindInTheBucketOfItsClass(t *testing.T) {
	buckets := naming.Buckets{naming.StorageArtifacts: "a", naming.StorageEdge: "e", naming.StorageCache: "c"}
	for kind, want := range map[naming.ObjectKind]string{
		naming.ObjectFunction: "a", naming.ObjectBytecode: "a", naming.ObjectPayloads: "a",
		naming.ObjectAssets: "e", naming.ObjectPages: "e", naming.ObjectBundles: "e",
		naming.ObjectFetchCache: "c", naming.ObjectTagClock: "c",
	} {
		if got := buckets.Holding(kind); got != want {
			t.Errorf("Holding(%s) = %q, want %q", kind, got, want)
		}
	}
	if got := (naming.Buckets{naming.StorageEdge: "e"}).Holding(naming.ObjectFetchCache); got != "" {
		t.Errorf("Holding(fetch-cache) with no cache bucket = %q, want none", got)
	}
}

func TestOnlyWhatTheAppCanRebuildExpiresFromTheCacheBucket(t *testing.T) {
	for _, kind := range naming.ExpiringObjectKinds {
		if kind.Class() != naming.StorageCache {
			t.Errorf("%s expires but lives in the %s bucket, not the cache bucket", kind, kind.Class())
		}
	}
	if slices.Contains(naming.ExpiringObjectKinds, naming.ObjectTagClock) {
		t.Error("the tag clock expires, so a release quiet for longer than the lifetime stops reaching other instances when it revalidates a tag")
	}
	for _, kind := range []naming.ObjectKind{naming.ObjectFetchCache, naming.ObjectUseCache} {
		if !slices.Contains(naming.ExpiringObjectKinds, kind) {
			t.Errorf("%s does not expire, so its entries outlive any use", kind)
		}
	}
}

func TestTheRuntimeLearnsTheStorageCoordinateAndEveryBucketButState(t *testing.T) {
	release := naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: naming.NewReleaseToken("b1", "f1")}
	buckets := naming.Buckets{naming.StorageState: "s", naming.StorageArtifacts: "a", naming.StorageEdge: "e", naming.StorageCache: "c"}
	want := map[string]string{
		"OCEL_STORAGE_COORDINATE": "shop/prod/web/" + release.Release.String(),
		"OCEL_ARTIFACTS_BUCKET":   "a",
		"OCEL_EDGE_BUCKET":        "e",
		"OCEL_CACHE_BUCKET":       "c",
	}
	if got := naming.StorageEnv(release, buckets); !maps.Equal(got, want) {
		t.Errorf("StorageEnv() = %v, want %v", got, want)
	}
	if got := naming.StorageEnv(release, naming.Buckets{naming.StorageEdge: "e"}); !maps.Equal(got, map[string]string{
		"OCEL_STORAGE_COORDINATE": "shop/prod/web/" + release.Release.String(),
		"OCEL_EDGE_BUCKET":        "e",
	}) {
		t.Errorf("StorageEnv() of an origin with only an edge bucket = %v, want the coordinate and that bucket", got)
	}
}

func TestAKeyOutsideEveryCoordinateKeyedKindIsRefused(t *testing.T) {
	for _, key := range readStorageKeys(t).Invalid {
		if got, err := naming.ParseObjectKey(key); err == nil {
			t.Errorf("ParseObjectKey(%q) = %+v, want an error", key, got)
		}
	}
}

func TestAStorageCoordinateIsTheProjectEnvAppAndReleaseTheRuntimeParses(t *testing.T) {
	for _, want := range readStorageKeys(t).Coordinates {
		if got := coordinateOf(t, want.Coordinate).StorageCoordinate(); got != want.Value {
			t.Errorf("StorageCoordinate() = %q, want %q", got, want.Value)
		}
	}
}

func TestEveryCoordinateKeyedKindLandsInOneStorageClass(t *testing.T) {
	want := map[naming.ObjectKind]naming.StorageClass{
		naming.ObjectFunction:    naming.StorageArtifacts,
		naming.ObjectBytecode:    naming.StorageArtifacts,
		naming.ObjectAssets:      naming.StorageEdge,
		naming.ObjectPages:       naming.StorageEdge,
		naming.ObjectRouteTables: naming.StorageEdge,
		naming.ObjectBundles:     naming.StorageEdge,
		naming.ObjectImageConfig: naming.StorageEdge,
		naming.ObjectFetchCache:  naming.StorageCache,
		naming.ObjectUseCache:    naming.StorageCache,
		naming.ObjectTagClock:    naming.StorageCache,
	}
	for _, kind := range naming.CoordinateObjectKinds {
		if got := kind.Class(); got != want[kind] {
			t.Errorf("%s.Class() = %q, want %q", kind, got, want[kind])
		}
	}
	if len(naming.CoordinateObjectKinds) != len(want) {
		t.Errorf("CoordinateObjectKinds = %v, want the %d kinds keyed by a coordinate", naming.CoordinateObjectKinds, len(want))
	}
	if naming.ObjectPayloads.Class() != naming.StorageArtifacts || slices.Contains(naming.CoordinateObjectKinds, naming.ObjectPayloads) {
		t.Errorf("payloads are tier-wide artifacts, never keyed by a coordinate")
	}
}

func TestTheISRKindsAreThePagesAndTheCacheKinds(t *testing.T) {
	want := []naming.ObjectKind{naming.ObjectPages, naming.ObjectFetchCache, naming.ObjectUseCache, naming.ObjectTagClock}
	if !slices.Equal(naming.ISRObjectKinds, want) {
		t.Errorf("ISRObjectKinds = %v, want %v", naming.ISRObjectKinds, want)
	}
}

func TestTheReleaseKeysOfOneCoordinateEachSitUnderTheirKindsReleasePrefix(t *testing.T) {
	release := naming.NewReleaseToken("dep1", "fp1")
	c := naming.Coordinate{Project: "shop", Env: "prod", App: "web", Name: "API Handler", Release: release}
	at := "shop/prod/web/" + release.String() + "/"
	for got, want := range map[string]string{
		c.FunctionArtifactKey("deadbeef"): "fn/" + at + "api-handler/deadbeef.zip",
		c.AssetKey("/static/app.js"):      "assets/" + at + "static/app.js",
		c.ImageConfigKey():                "image-config/" + at + "image-config.json",
		c.RouteTableKey("ab"):             "route-tables/" + at + "ab.json",
		c.BundleKey("bundle.json"):        "bundles/" + at + "bundle.json",
	} {
		if got != want {
			t.Errorf("key = %q, want %q", got, want)
		}
	}
}

func TestAStorageCoordinateParsesBackToTheCoordinateItNames(t *testing.T) {
	fixture := readStorageKeys(t)
	for _, want := range fixture.Coordinates {
		got, err := naming.ParseStorageCoordinate(want.Value)
		if err != nil || got != coordinateOf(t, want.Coordinate) {
			t.Errorf("ParseStorageCoordinate(%q) = %+v, %v, want %+v", want.Value, got, err, want.Coordinate)
		}
	}
	for _, value := range fixture.InvalidCoordinates {
		if got, err := naming.ParseStorageCoordinate(value); err == nil {
			t.Errorf("ParseStorageCoordinate(%q) = %+v, want an error", value, got)
		}
	}
}

func TestTheKindOfAKeyIsItsFirstSegmentWhetherOrNotACoordinateFollows(t *testing.T) {
	for key, want := range map[string]naming.ObjectKind{
		"fn/shop/prod/web/r1a2b3c4d/api/5e1f.zip":     naming.ObjectFunction,
		naming.PayloadKey("upload-completer", "5e1f"): naming.ObjectPayloads,
	} {
		if got, err := naming.ParseObjectKind(key); err != nil || got != want {
			t.Errorf("ParseObjectKind(%q) = %q, %v, want %q", key, got, err, want)
		}
	}
	for _, key := range []string{"isr/shop/prod/web/r1a2b3c4d/x", "prod/shop/web/r1a2b3c4d/assets/x", ""} {
		if got, err := naming.ParseObjectKind(key); err == nil {
			t.Errorf("ParseObjectKind(%q) = %q, want an error", key, got)
		}
	}
}

func TestAPayloadKeyIsTierWideAndNamedByItsDigest(t *testing.T) {
	if got, want := naming.PayloadKey("upload-completer", "5e1f"), "payloads/upload-completer/5e1f.zip"; got != want {
		t.Errorf("PayloadKey() = %q, want %q", got, want)
	}
}
