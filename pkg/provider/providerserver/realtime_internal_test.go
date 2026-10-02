package providerserver

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func realtimeManifestResource(name string, config *resourcesv1.RealtimeConfig) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "realtime--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: name},
		Config:      &contractv1.ManifestResource_Realtime{Realtime: config},
	}
}

func TestARealtimeStoreIsHandedToTheVendorWithEachChannelAndItsTokenTTL(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(realtimeManifestResource("app", &resourcesv1.RealtimeConfig{
		TokenTtl: durationpb.New(90 * time.Second),
		Channels: []*resourcesv1.RealtimeChannel{
			{
				Pattern:   "orders/:orderId",
				Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
				Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
				Source:    "src/realtime.ts:8",
			},
			{
				Pattern:   "rooms/:roomId",
				Wildcard:  true,
				Schema:    `{"type":"object"}`,
				Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC,
				Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE,
			},
		},
	}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	want := &provider.RealtimeSpec{
		TokenTTL: 90 * time.Second,
		Channels: []provider.ChannelSpec{
			{Pattern: "orders/:orderId", Subscribe: realtime.ChannelAccessRule, Publish: realtime.ChannelAccessServer},
			{Pattern: "rooms/:roomId", Wildcard: true, Schema: `{"type":"object"}`, Subscribe: realtime.ChannelAccessPublic, Publish: realtime.ChannelAccessRule},
		},
	}
	if resource.Type != provider.BindingRealtime || !reflect.DeepEqual(resource.Realtime, want) {
		t.Errorf("manifestResource() = %+v (realtime %+v), want a realtime resource with %+v", resource, resource.Realtime, want)
	}
}

func TestARealtimeStoreNamingNoTokenTTLIsHandedToTheVendorWithSixtySeconds(t *testing.T) {
	t.Parallel()

	for _, config := range []*resourcesv1.RealtimeConfig{nil, {}} {
		resource, err := manifestResource(realtimeManifestResource("app", config))
		if err != nil {
			t.Fatalf("manifestResource: %v", err)
		}
		if resource.Realtime == nil || resource.Realtime.TokenTTL != 60*time.Second {
			t.Errorf("manifestResource(%v).Realtime = %+v, want a token ttl of 60s", config, resource.Realtime)
		}
	}
}

func TestARealtimeStoreNoVendorCouldRunIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	overlapping := &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{
		{Pattern: "orders/:orderId", Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE, Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER},
		{Pattern: "orders/latest", Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE, Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER},
	}}
	for _, tc := range []struct {
		name    string
		message *contractv1.ManifestResource
		says    []string
	}{
		{name: "a non-positive token ttl", message: realtimeManifestResource("app", &resourcesv1.RealtimeConfig{TokenTtl: durationpb.New(-time.Second)}), says: []string{"realtime app", "token ttl"}},
		{name: "a malformed pattern", message: realtimeManifestResource("app", &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{{Pattern: "order_items/:id"}}}), says: []string{"realtime app", `channel "order_items/:id"`}},
		{name: "overlapping patterns", message: realtimeManifestResource("app", overlapping), says: []string{`channel "orders/latest"`, "overlaps"}},
		{name: "a name that begins no channel", message: realtimeManifestResource("App_Live", &resourcesv1.RealtimeConfig{}), says: []string{`"App_Live"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := manifestResource(tc.message)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("manifestResource() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			for _, said := range tc.says {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("manifestResource() = %q, want it to say %q", err, said)
				}
			}
		})
	}
}

func TestARealtimeConfigOnAResourceOfAnotherTypeIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	message := realtimeManifestResource("cache", &resourcesv1.RealtimeConfig{})
	message.Resource.Type = resourcesv1.ResourceType_RESOURCE_TYPE_KV
	_, err := manifestResource(message)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("manifestResource() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "kv cache") || !strings.Contains(err.Error(), "realtime") {
		t.Errorf("manifestResource() = %q, want it to name kv cache and the realtime config it carries", err)
	}
}

func TestEverySubscribeAndPublishARealtimeStoreIsAcceptedWithReachesTheVendorWithAnAccess(t *testing.T) {
	t.Parallel()

	subscribes := []resourcesv1.RealtimeSubscribe{resourcesv1.RealtimeSubscribe(7)}
	for value := range resourcesv1.RealtimeSubscribe_name {
		subscribes = append(subscribes, resourcesv1.RealtimeSubscribe(value))
	}
	publishes := []resourcesv1.RealtimePublish{resourcesv1.RealtimePublish(7)}
	for value := range resourcesv1.RealtimePublish_name {
		publishes = append(publishes, resourcesv1.RealtimePublish(value))
	}
	for _, subscribe := range subscribes {
		for _, publish := range publishes {
			resource, err := manifestResource(realtimeManifestResource("app", &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{
				{Pattern: "orders/:orderId", Subscribe: subscribe, Publish: publish},
			}}))
			if err != nil {
				continue
			}
			if channel := resource.Realtime.Channels[0]; channel.Subscribe == "" || channel.Publish == "" {
				t.Errorf("manifestResource(subscribe %v, publish %v) handed the vendor %+v, want an access for each or a refusal", subscribe, publish, channel)
			}
		}
	}
}
