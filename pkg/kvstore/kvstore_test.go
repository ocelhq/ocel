package kvstore

import (
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestTheDefaultVersionIsOneAStoreMayName(t *testing.T) {
	t.Parallel()

	if DefaultVersion != "9" || !slices.Contains(versions, DefaultVersion) {
		t.Errorf("DefaultVersion = %q among %v, want \"9\", a version a store may name", DefaultVersion, versions)
	}
}

func TestAStoreEvictsUnderTheEightValkeyPolicies(t *testing.T) {
	t.Parallel()

	want := []string{"allkeys-lfu", "allkeys-lru", "allkeys-random", "noeviction", "volatile-lfu", "volatile-lru", "volatile-random", "volatile-ttl"}
	if got := slices.Sorted(slices.Values(evictions)); !slices.Equal(got, want) {
		t.Errorf("evictions = %v, want %v", got, want)
	}
}

func TestAStoreRunsOnlyAVersionItKnows(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"8", "9"} {
		if err := RefuseVersion(version); err != nil {
			t.Errorf("RefuseVersion(%q) = %v, want it accepted", version, err)
		}
	}
	for _, version := range []string{"", "7", "9.0", "10"} {
		err := RefuseVersion(version)
		if err == nil || !strings.Contains(err.Error(), "8, 9") {
			t.Errorf("RefuseVersion(%q) = %v, want it refused naming the versions 8, 9", version, err)
		}
	}
}

func TestAStoreEvictsOnlyByAValkeyPolicy(t *testing.T) {
	t.Parallel()

	if err := RefuseEviction("volatile-ttl"); err != nil {
		t.Errorf("RefuseEviction(volatile-ttl) = %v, want it accepted", err)
	}
	for _, eviction := range []string{"", "lru", "ALLKEYS-LRU"} {
		err := RefuseEviction(eviction)
		if err == nil || !strings.Contains(err.Error(), "allkeys-lru") {
			t.Errorf("RefuseEviction(%q) = %v, want it refused naming the policies", eviction, err)
		}
	}
}

func TestAStoreDeclaringNothingRunsTheDefaultVersionAndMemoryWithTheEnginesEviction(t *testing.T) {
	t.Parallel()

	settings, err := ReadSettings(nil)
	if err != nil {
		t.Fatalf("ReadSettings(nil) = %v", err)
	}
	if settings != (Settings{Version: "9", MemoryBytes: 256 << 20}) {
		t.Errorf("ReadSettings(nil) = %+v, want version 9 with 256mb and no eviction policy", settings)
	}
}

func TestAStoreRunsTheVersionMemoryAndEvictionItDeclares(t *testing.T) {
	t.Parallel()

	settings, err := ReadSettings(&resourcesv1.KvConfig{Version: "8", Memory: "1gb", Eviction: "allkeys-lfu"})
	if err != nil {
		t.Fatalf("ReadSettings() = %v", err)
	}
	if settings != (Settings{Version: "8", MemoryBytes: 1 << 30, Eviction: "allkeys-lfu"}) {
		t.Errorf("ReadSettings() = %+v, want version 8 with 1gb evicting by allkeys-lfu", settings)
	}
}

func TestAStoreDeclaringWhatItCannotRunIsRefusedSayingWhat(t *testing.T) {
	t.Parallel()

	for want, config := range map[string]*resourcesv1.KvConfig{
		`version "7"`:      {Version: "7"},
		`eviction "lru"`:   {Eviction: "lru"},
		`memory is "lots"`: {Memory: "lots"},
	} {
		if _, err := ReadSettings(config); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadSettings(%v) = %v, want it refused saying %s", config, err, want)
		}
	}
}
