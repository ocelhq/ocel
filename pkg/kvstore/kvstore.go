package kvstore

import (
	"fmt"
	"slices"
	"strings"
)

const DefaultVersion = "9"

var versions = []string{"8", "9"}

var evictions = []string{
	"noeviction",
	"allkeys-lru",
	"allkeys-lfu",
	"allkeys-random",
	"volatile-lru",
	"volatile-lfu",
	"volatile-random",
	"volatile-ttl",
}

func RefuseVersion(version string) error {
	if !slices.Contains(versions, version) {
		return fmt.Errorf("version %q is none a store runs: it runs version %s", version, strings.Join(versions, ", "))
	}
	return nil
}

func RefuseEviction(eviction string) error {
	if !slices.Contains(evictions, eviction) {
		return fmt.Errorf("eviction %q is no policy a store evicts by: it evicts by one of %s", eviction, strings.Join(evictions, ", "))
	}
	return nil
}
