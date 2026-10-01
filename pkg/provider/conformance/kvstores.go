package conformance

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunKVStores(t *testing.T, facts provider.Facts) {
	t.Helper()

	for _, fault := range kvStoreFaults(facts) {
		t.Error(fault)
	}
}

func kvStoreFaults(facts provider.Facts) []string {
	if slices.Contains(facts.Bindings, provider.BindingKV) {
		return nil
	}
	err := providerserver.RefuseUnsupportedKVStores(facts, versionlessKVManifest())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		return []string{fmt.Sprintf("a deploy declaring a kv store on a provider whose Facts.Bindings has no kv passed preflight with %v, want a refusal with code %s", err, refusal.CodeUnsupported)}
	}
	return nil
}

func versionlessKVManifest() *contractv1.Manifest {
	return &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{{
		LogicalName: "kv--cache",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: "cache"},
		Config: &contractv1.ManifestResource_Kv{Kv: &resourcesv1.KvConfig{
			Entries: []*resourcesv1.KvEntry{
				{Name: "session", Pattern: "session/:id", Shape: resourcesv1.KvShape_KV_SHAPE_JSON},
			},
		}},
	}}}
}
