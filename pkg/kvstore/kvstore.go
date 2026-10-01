package kvstore

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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

type Settings struct {
	Version     string
	MemoryBytes int64
	Eviction    string
}

func ReadSettings(config *resourcesv1.KvConfig) (Settings, error) {
	version := cmp.Or(config.GetVersion(), DefaultVersion)
	if err := RefuseVersion(version); err != nil {
		return Settings{}, err
	}
	if eviction := config.GetEviction(); eviction != "" {
		if err := RefuseEviction(eviction); err != nil {
			return Settings{}, err
		}
	}
	memory, err := ParseMemory(cmp.Or(config.GetMemory(), DefaultMemory))
	if err != nil {
		return Settings{}, err
	}
	return Settings{Version: version, MemoryBytes: memory, Eviction: config.GetEviction()}, nil
}
