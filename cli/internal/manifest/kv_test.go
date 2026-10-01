package manifest

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func kvStore(name, source string, config *resourcesv1.KvConfig, entries ...declaredEntry) declaredResource {
	if config == nil {
		config = &resourcesv1.KvConfig{}
	}
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: name, KV: config, KVEntries: entries, Source: source}
}

func kvEntry(name, pattern, source string) declaredEntry {
	return declaredEntry{Name: name, Pattern: pattern, Shape: resourcesv1.KvShape_KV_SHAPE_TEXT, Source: source}
}

func findKV(m *contractv1.Manifest, logical string) *resourcesv1.KvConfig {
	for _, r := range m.GetResources() {
		if r.GetLogicalName() == logical {
			return r.GetKv()
		}
	}
	return nil
}

func TestAKVStoreReachesTheManifestWithItsEntriesLeavingDefaultsToTheProvider(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Eviction: "allkeys-lru"},
			declaredEntry{Name: "requests", Pattern: "requests/:userId", Shape: resourcesv1.KvShape_KV_SHAPE_COUNTER, Source: "src/cache.ts:5"},
			declaredEntry{Name: "session", Pattern: "session/:id", Shape: resourcesv1.KvShape_KV_SHAPE_JSON, Source: "src/cache.ts:6"},
		),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	cache := findKV(m, "kv--cache")
	if cache == nil {
		t.Fatalf("manifest resources = %v, want kv--cache", m.GetResources())
	}
	if cache.GetVersion() != "" || cache.GetMemory() != "" {
		t.Errorf("version, memory = %q, %q, want neither: the provider defaults them", cache.GetVersion(), cache.GetMemory())
	}
	if cache.GetEviction() != "allkeys-lru" {
		t.Errorf("eviction = %q, want allkeys-lru as declared", cache.GetEviction())
	}
	entries := cache.GetEntries()
	if len(entries) != 2 || entries[0].GetName() != "requests" || entries[0].GetShape() != resourcesv1.KvShape_KV_SHAPE_COUNTER || entries[1].GetPattern() != "session/:id" {
		t.Errorf("entries = %v, want requests and session with their patterns and shapes", entries)
	}
}

func TestAKVStoreDeclaringAVersionAndMemoryKeepsThem(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Version: "8", Memory: "1gb"}),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	if cache := findKV(m, "kv--cache"); cache.GetVersion() != "8" || cache.GetMemory() != "1gb" {
		t.Errorf("kv--cache = %v, want version 8 and 1gb as declared", cache)
	}
}

func TestABadKVStoreIsRefusedAtTheLineDeclaringIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		store declaredResource
		at    string
		says  []string
	}{
		{
			name:  "an unknown version",
			store: kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Version: "7"}),
			at:    "src/cache.ts:2", says: []string{`version "7"`, "8, 9"},
		},
		{
			name:  "an unknown eviction",
			store: kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Eviction: "lru"}),
			at:    "src/cache.ts:2", says: []string{`eviction "lru"`, "allkeys-lru"},
		},
		{
			name:  "memory below the floor",
			store: kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Memory: "8mb"}),
			at:    "src/cache.ts:2", says: []string{"8mb", "32mb", "32gb"},
		},
		{
			name:  "memory above the ceiling",
			store: kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Memory: "64gb"}),
			at:    "src/cache.ts:2", says: []string{"64gb", "32mb", "32gb"},
		},
		{
			name:  "memory that is no size",
			store: kvStore("cache", "src/cache.ts:2", &resourcesv1.KvConfig{Memory: "lots"}),
			at:    "src/cache.ts:2", says: []string{"lots", "kb, mb or gb"},
		},
		{
			name:  "a malformed pattern",
			store: kvStore("cache", "src/cache.ts:2", nil, kvEntry("session", "session/{id}", "src/cache.ts:7")),
			at:    "src/cache.ts:7", says: []string{`entry "session"`, "hash tag"},
		},
		{
			name:  "an entry name TypeScript reserves",
			store: kvStore("cache", "src/cache.ts:2", nil, kvEntry("connectionString", "conn", "src/cache.ts:4")),
			at:    "src/cache.ts:4", says: []string{`"connectionString"`, "TypeScript"},
		},
		{
			name:  "an entry named client",
			store: kvStore("cache", "src/cache.ts:2", nil, kvEntry("client", "clients/:id", "src/cache.ts:4")),
			at:    "src/cache.ts:4", says: []string{`"client"`, "reserve"},
		},
		{
			name:  "an entry named then",
			store: kvStore("cache", "src/cache.ts:2", nil, kvEntry("then", "next/:id", "src/cache.ts:4")),
			at:    "src/cache.ts:4", says: []string{`"then"`, "TypeScript"},
		},
		{
			name:  "an entry name no language can hold",
			store: kvStore("cache", "src/cache.ts:2", nil, kvEntry("user-sessions", "sessions/:id", "src/cache.ts:4")),
			at:    "src/cache.ts:4", says: []string{`"user-sessions"`, "letter"},
		},
		{
			name: "an entry with no shape",
			store: kvStore("cache", "src/cache.ts:2", nil,
				declaredEntry{Name: "session", Pattern: "session/:id", Source: "src/cache.ts:4"}),
			at: "src/cache.ts:4", says: []string{`entry "session"`, "shape"},
		},
		{
			name: "two entries of one name",
			store: kvStore("cache", "src/cache.ts:2", nil,
				kvEntry("session", "session/:id", "src/cache.ts:4"),
				kvEntry("session", "sessions/:id", "src/cache.ts:5")),
			at: "src/cache.ts:5", says: []string{`entry "session"`, "src/cache.ts:4"},
		},
		{
			name:  "a store name that is no resource name",
			store: kvStore("Cache_Store", "src/cache.ts:2", nil),
			at:    "src/cache.ts:2", says: []string{`"Cache_Store"`, "lowercase"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{tc.store}, nil)
			var invalid *InvalidDeclarationError
			if !errors.As(err, &invalid) || invalid.Source != tc.at {
				t.Fatalf("assemble() = %v, want it refused at %s", err, tc.at)
			}
			for _, said := range tc.says {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("assemble() = %q, want it to say %q", err, said)
				}
			}
		})
	}
}

func TestTwoOverlappingPatternsInOneStoreAreRefusedNamingBothSites(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		kvStore("cache", "src/cache.ts:2", nil,
			kvEntry("session", "session/:id", "src/cache.ts:4"),
			kvEntry("current", "session/current", "src/cache.ts:5"),
		),
	}, nil)
	if err == nil {
		t.Fatal("assemble() = nil, want the overlap refused")
	}
	for _, said := range []string{"src/cache.ts:4", "src/cache.ts:5", `"session/:id"`, `"session/current"`, "overlap"} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("assemble() = %q, want it to say %q", err, said)
		}
	}
}

func TestPatternsThatOverlapAcrossTwoStoresAreTheirOwnKeys(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		kvStore("cache", "src/cache.ts:2", nil, kvEntry("session", "session/:id", "src/cache.ts:4")),
		kvStore("sessions", "src/sessions.ts:2", nil, kvEntry("session", "session/:id", "src/sessions.ts:4")),
	}, nil)
	if err != nil {
		t.Errorf("assemble() = %v, want two stores free to name the same pattern: each is its own engine", err)
	}
}

func TestAnEntryIsAttributedToItsOwnLineOrElseToItsStores(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	declared := declaredResources(root, []declaration.Resource{{
		Type:   resourcesv1.ResourceType_RESOURCE_TYPE_KV,
		Name:   "cache",
		Source: filepath.Join(root, "src", "cache.go") + ":10",
		KV: &resourcesv1.KvConfig{Entries: []*resourcesv1.KvEntry{
			{Name: "requests", Pattern: "requests/:user", Shape: resourcesv1.KvShape_KV_SHAPE_COUNTER, Source: filepath.Join(root, "src", "limits.go") + ":22"},
			{Name: "session", Pattern: "session/:id", Shape: resourcesv1.KvShape_KV_SHAPE_JSON},
		}},
	}})

	entries := declared[0].KVEntries
	if len(entries) != 2 || entries[0].Source != "src/limits.go:22" || entries[1].Source != "src/cache.go:10" {
		t.Errorf("entries = %+v, want requests at src/limits.go:22 and session at its store's src/cache.go:10", entries)
	}
}
