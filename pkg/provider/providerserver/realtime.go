package providerserver

import (
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnsupportedRealtime(facts provider.Facts, manifest *contractv1.Manifest) error {
	if slices.Contains(facts.Bindings, provider.BindingRealtime) {
		return nil
	}
	for _, resource := range manifest.GetResources() {
		if resource.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME {
			return refusal.Refuse(refusal.CodeUnsupported,
				"this project declares realtime %q, and realtime is unsupported on %s: it runs no transport for channels",
				resource.GetResource().GetName(), facts.Vendor)
		}
	}
	return nil
}

func readRealtimeSpec(name string, config *resourcesv1.RealtimeConfig) (*provider.RealtimeSpec, error) {
	if err := realtime.RefuseConfig(name, config); err != nil {
		return nil, err
	}
	spec := &provider.RealtimeSpec{TokenTTL: realtime.DefaultTokenTTL}
	if ttl := config.GetTokenTtl(); ttl != nil {
		spec.TokenTTL = ttl.AsDuration()
	}
	for _, channel := range config.GetChannels() {
		subscribe, _ := realtime.FindSubscribeAccess(channel.GetSubscribe())
		publish, _ := realtime.FindPublishAccess(channel.GetPublish())
		spec.Channels = append(spec.Channels, provider.ChannelSpec{
			Pattern:   channel.GetPattern(),
			Wildcard:  channel.GetWildcard(),
			Schema:    channel.GetSchema(),
			Subscribe: subscribe,
			Publish:   publish,
		})
	}
	return spec, nil
}
