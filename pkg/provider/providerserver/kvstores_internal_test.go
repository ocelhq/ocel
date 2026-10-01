package providerserver

import (
	"errors"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func kvManifestResource(config *resourcesv1.KvConfig) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "kv--cache",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: "cache"},
		Config:      &contractv1.ManifestResource_Kv{Kv: config},
	}
}

func TestAKVStoreNamingNoVersionOrMemoryIsHandedToTheVendorAtTheDefaults(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(kvManifestResource(&resourcesv1.KvConfig{Eviction: "allkeys-lru"}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	want := provider.KVSpec{Version: "9", Eviction: "allkeys-lru", MemoryBytes: 256 << 20}
	if resource.Type != provider.BindingKV || resource.KV == nil || *resource.KV != want {
		t.Errorf("manifestResource() = %+v (kv %+v), want a kv resource with %+v", resource, resource.KV, want)
	}
}

func TestAKVStoreNamingAVersionKeepsIt(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(kvManifestResource(&resourcesv1.KvConfig{Version: "8", Memory: "1gb"}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	if resource.KV.Version != "8" || resource.KV.MemoryBytes != 1<<30 {
		t.Errorf("KV = %+v, want version 8 and 1 GiB as the manifest names them", resource.KV)
	}
}

func TestAKVStoreNamingWhatNoStoreRunsIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config *resourcesv1.KvConfig
		says   string
	}{
		{name: "memory that is no size", config: &resourcesv1.KvConfig{Memory: "lots"}, says: "lots"},
		{name: "memory out of range", config: &resourcesv1.KvConfig{Memory: "64gb"}, says: "32gb"},
		{name: "an unknown version", config: &resourcesv1.KvConfig{Version: "7"}, says: `version "7"`},
		{name: "an unknown eviction", config: &resourcesv1.KvConfig{Eviction: "lru"}, says: `eviction "lru"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := manifestResource(kvManifestResource(tc.config))
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("manifestResource() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("manifestResource() = %q, want it to say %q", err, tc.says)
			}
		})
	}
}
