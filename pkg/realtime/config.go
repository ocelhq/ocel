package realtime

import (
	"cmp"
	"fmt"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type InvalidChannelError struct {
	Pattern string
	Source  string
	Reason  string
}

func (e *InvalidChannelError) Error() string {
	return fmt.Sprintf("channel %q %s", e.Pattern, e.Reason)
}

func RefuseConfig(name string, config *resourcesv1.RealtimeConfig) error {
	if err := refuseNamespace(name); err != nil {
		return err
	}
	if ttl := config.GetTokenTtl(); ttl != nil {
		if err := refuseTokenTTL(ttl.AsDuration()); err != nil {
			return err
		}
	}
	return refuseChannels(config.GetChannels())
}

func refuseChannels(channels []*resourcesv1.RealtimeChannel) error {
	type parsedChannel struct {
		channel *resourcesv1.RealtimeChannel
		pattern Pattern
	}
	parsed := make([]parsedChannel, 0, len(channels))
	for _, channel := range channels {
		pattern, err := ParsePattern(channel.GetPattern())
		if err != nil {
			return refuseChannel(channel, "%s", err)
		}
		if channel.GetWildcard() && !pattern.HasParameters() {
			return refuseChannel(channel, "sets wildcard, and a pattern with no params has no trailing param a subscriber could leave off")
		}
		if channel.GetSubscribe() == resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_UNSPECIFIED {
			return refuseChannel(channel, "declares no subscribe: it is a rule or \"public\"")
		}
		if _, found := FindSubscribeAccess(channel.GetSubscribe()); !found {
			return refuseChannel(channel, "declares subscribe %v, which no channel takes", channel.GetSubscribe())
		}
		if channel.GetPublish() == resourcesv1.RealtimePublish_REALTIME_PUBLISH_UNSPECIFIED {
			return refuseChannel(channel, "declares no publish: it is a rule, or the server alone publishes")
		}
		if _, found := FindPublishAccess(channel.GetPublish()); !found {
			return refuseChannel(channel, "declares publish %v, which no channel takes", channel.GetPublish())
		}
		parsed = append(parsed, parsedChannel{channel, pattern})
	}
	for i, later := range parsed {
		for _, prior := range parsed[:i] {
			if prior.pattern.Overlaps(later.pattern) {
				return refuseChannel(later.channel,
					"overlaps pattern %q declared at %s: some channel would match both, so neither pattern's rules could tell its channels from the other's",
					prior.channel.GetPattern(), sourceOrUnknown(prior.channel))
			}
		}
	}
	for i, wildcard := range parsed {
		if !wildcard.channel.GetWildcard() {
			continue
		}
		for j, other := range parsed {
			if i != j && wildcard.pattern.WildcardCovers(other.pattern) {
				return refuseChannel(wildcard.channel,
					"sets wildcard, and a subscriber leaving off its trailing params would also receive the channels of pattern %q declared at %s, which its rules never checked",
					other.channel.GetPattern(), sourceOrUnknown(other.channel))
			}
		}
	}
	return nil
}

func refuseChannel(channel *resourcesv1.RealtimeChannel, format string, args ...any) error {
	return &InvalidChannelError{Pattern: channel.GetPattern(), Source: channel.GetSource(), Reason: fmt.Sprintf(format, args...)}
}

func sourceOrUnknown(channel *resourcesv1.RealtimeChannel) string {
	return cmp.Or(channel.GetSource(), "<unknown source>")
}
