package naming_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
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
		if got.Kind != naming.ObjectKind(want.Kind) || got.Project != want.Project || got.Env != want.Env ||
			got.App != want.App || got.Release.String() != want.Release || got.Rest != want.Rest {
			t.Errorf("ParseObjectKey(%q) = %+v, want %+v", want.Key, got, want)
		}
	}
}

func TestAnObjectKeyIsTheKindThenTheCoordinateThenTheRest(t *testing.T) {
	for _, want := range readStorageKeys(t).Keys {
		if want.Rest == "" {
			continue
		}
		coordinate := coordinateOf(t, want.fixtureCoordinate)
		if got := coordinate.ObjectKey(naming.ObjectKind(want.Kind), want.Rest); got != want.Key {
			t.Errorf("ObjectKey(%s, %q) = %q, want %q", want.Kind, want.Rest, got, want.Key)
		}
	}
}

func TestAStoragePrefixStopsAtTheFirstFieldTheCoordinateLeavesEmpty(t *testing.T) {
	for _, want := range readStorageKeys(t).Prefixes {
		coordinate := coordinateOf(t, want.Coordinate)
		if got := coordinate.StoragePrefix(naming.ObjectKind(want.Kind)); got != want.Prefix {
			t.Errorf("StoragePrefix(%s) of %+v = %q, want %q", want.Kind, want.Coordinate, got, want.Prefix)
		}
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
		c.AssetPrefix():                   "assets/" + at[:len(at)-1],
		c.PagesPrefix():                   "pages/" + at[:len(at)-1],
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
