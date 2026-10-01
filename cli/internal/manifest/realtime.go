package manifest

import (
	"cmp"
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

func refuseRealtime(d declaredResource) error {
	if err := realtime.RefuseNamespace(d.Name); err != nil {
		return refuse(d, "%s", err)
	}
	if ttl := d.Realtime.GetTokenTtl(); ttl != nil {
		if err := realtime.RefuseTokenTTL(ttl.AsDuration()); err != nil {
			return refuse(d, "%s", err)
		}
	}
	return refuseRealtimeChannels(d)
}

func refuseRealtimeChannels(d declaredResource) error {
	type parsedChannel struct {
		channel declaredChannel
		pattern realtime.Pattern
	}
	parsed := make([]parsedChannel, 0, len(d.RealtimeChannels))
	for _, channel := range d.RealtimeChannels {
		pattern, err := realtime.ParsePattern(channel.Pattern)
		if err != nil {
			return refuseChannel(d, channel, "%s", err)
		}
		if channel.Wildcard && !pattern.HasParameters() {
			return refuseChannel(d, channel, "sets wildcard, and a pattern with no params has no trailing param a subscriber could leave off")
		}
		if channel.Subscribe == resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_UNSPECIFIED {
			return refuseChannel(d, channel, "declares no subscribe: it is a rule or \"public\"")
		}
		if channel.Publish == resourcesv1.RealtimePublish_REALTIME_PUBLISH_UNSPECIFIED {
			return refuseChannel(d, channel, "declares no publish: it is a rule, or the server alone publishes")
		}
		parsed = append(parsed, parsedChannel{channel, pattern})
	}
	for i, later := range parsed {
		for _, prior := range parsed[:i] {
			if prior.pattern.Overlaps(later.pattern) {
				return refuseChannel(d, later.channel,
					"overlaps pattern %q declared at %s: some channel would match both, so neither pattern's rules could tell its channels from the other's",
					prior.channel.Pattern, sourceOrUnknown(prior.channel.Source))
			}
		}
	}
	for i, wildcard := range parsed {
		if !wildcard.channel.Wildcard {
			continue
		}
		for j, other := range parsed {
			if i != j && wildcard.pattern.WildcardCovers(other.pattern) {
				return refuseChannel(d, wildcard.channel,
					"sets wildcard, and a subscriber leaving off its trailing params would also receive the channels of pattern %q declared at %s, which its rules never checked",
					other.channel.Pattern, sourceOrUnknown(other.channel.Source))
			}
		}
	}
	return nil
}

func refuseChannel(d declaredResource, channel declaredChannel, format string, args ...any) error {
	return &InvalidDeclarationError{
		Source: cmp.Or(channel.Source, d.Source),
		Reason: fmt.Sprintf("channel %q of %s ", channel.Pattern, d.label()) + fmt.Sprintf(format, args...),
	}
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
