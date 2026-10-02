package manifest

import (
	"cmp"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/realtime"
)

type declaredChannel struct {
	Pattern   string
	Wildcard  bool
	Schema    string
	Subscribe resourcesv1.RealtimeSubscribe
	Publish   resourcesv1.RealtimePublish
	Source    string
}

func declaredChannels(configDir string, config *resourcesv1.RealtimeConfig, resourceSource string) []declaredChannel {
	channels := make([]declaredChannel, 0, len(config.GetChannels()))
	for _, channel := range config.GetChannels() {
		source := resourceSource
		if site, ok := attribution.DeclaringSite(configDir, channel.GetSource()); ok {
			source = site.String()
		}
		channels = append(channels, declaredChannel{
			Pattern:   channel.GetPattern(),
			Wildcard:  channel.GetWildcard(),
			Schema:    channel.GetSchema(),
			Subscribe: channel.GetSubscribe(),
			Publish:   channel.GetPublish(),
			Source:    source,
		})
	}
	return channels
}

func refuseRealtime(d declaredResource, config *resourcesv1.RealtimeConfig) error {
	err := realtime.RefuseConfig(d.Name, config)
	var channel *realtime.InvalidChannelError
	if errors.As(err, &channel) {
		return &InvalidDeclarationError{
			Source: cmp.Or(channel.Source, d.Source),
			Reason: fmt.Sprintf("channel %q of %s %s", channel.Pattern, d.label(), channel.Reason),
		}
	}
	if err != nil {
		return refuse(d, "%s", err)
	}
	return nil
}

func manifestRealtime(d declaredResource) *resourcesv1.RealtimeConfig {
	channels := make([]*resourcesv1.RealtimeChannel, 0, len(d.RealtimeChannels))
	for _, channel := range d.RealtimeChannels {
		channels = append(channels, &resourcesv1.RealtimeChannel{
			Pattern:   channel.Pattern,
			Wildcard:  channel.Wildcard,
			Schema:    channel.Schema,
			Subscribe: channel.Subscribe,
			Publish:   channel.Publish,
			Source:    channel.Source,
		})
	}
	ttl := d.Realtime.GetTokenTtl()
	if ttl == nil {
		ttl = durationpb.New(realtime.DefaultTokenTTL)
	}
	return &resourcesv1.RealtimeConfig{Channels: channels, TokenTtl: ttl}
}
