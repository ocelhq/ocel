package kvstore

import (
	"slices"
	"strings"
	"testing"
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
