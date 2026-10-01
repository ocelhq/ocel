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

func RunRealtime(t *testing.T, facts provider.Facts) {
	t.Helper()

	for _, fault := range realtimeFaults(facts) {
		t.Error(fault)
	}
}

func realtimeFaults(facts provider.Facts) []string {
	if slices.Contains(facts.Bindings, provider.BindingRealtime) {
		return nil
	}
	err := providerserver.RefuseUnsupportedRealtime(facts, realtimeManifest())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		return []string{fmt.Sprintf("a deploy declaring realtime on a provider whose Facts.Bindings has no realtime passed preflight with %v, want a refusal with code %s", err, refusal.CodeUnsupported)}
	}
	return nil
}

func realtimeManifest() *contractv1.Manifest {
	return &contractv1.Manifest{Slug: "shop", Resources: []*contractv1.ManifestResource{{
		LogicalName: "realtime--app",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: "app"},
		Config: &contractv1.ManifestResource_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			Channels: []*resourcesv1.RealtimeChannel{{
				Pattern:   "orders/:orderId",
				Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
				Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
			}},
		}},
	}}}
}
