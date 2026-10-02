package realtime

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func ruleChannel(pattern, source string) *resourcesv1.RealtimeChannel {
	return &resourcesv1.RealtimeChannel{
		Pattern:   pattern,
		Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
		Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
		Source:    source,
	}
}

func TestAConfigOfDistinctChannelsAndATTLInRangeIsAccepted(t *testing.T) {
	t.Parallel()

	config := &resourcesv1.RealtimeConfig{
		TokenTtl: durationpb.New(90 * time.Second),
		Channels: []*resourcesv1.RealtimeChannel{ruleChannel("orders/:orderId", ""), ruleChannel("rooms/:roomId", "")},
	}
	if err := RefuseConfig("app", config); err != nil {
		t.Errorf("RefuseConfig() = %v, want it accepted", err)
	}
}

func TestAConfigNoChannelCouldRunIsRefusedNamingTheChannelAndItsSource(t *testing.T) {
	t.Parallel()

	noSubscribe := ruleChannel("orders/:orderId", "src/realtime.ts:8")
	noSubscribe.Subscribe = resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_UNSPECIFIED
	noPublish := ruleChannel("orders/:orderId", "src/realtime.ts:8")
	noPublish.Publish = resourcesv1.RealtimePublish_REALTIME_PUBLISH_UNSPECIFIED
	unknownSubscribe := ruleChannel("orders/:orderId", "src/realtime.ts:8")
	unknownSubscribe.Subscribe = resourcesv1.RealtimeSubscribe(7)
	unknownPublish := ruleChannel("orders/:orderId", "src/realtime.ts:8")
	unknownPublish.Publish = resourcesv1.RealtimePublish(7)
	wildcard := ruleChannel("status/live", "src/realtime.ts:8")
	wildcard.Wildcard = true
	for _, tc := range []struct {
		name     string
		channels []*resourcesv1.RealtimeChannel
		pattern  string
		source   string
		says     []string
	}{
		{name: "a malformed pattern", channels: []*resourcesv1.RealtimeChannel{ruleChannel("a/b/c/d/:e", "src/realtime.ts:8")}, pattern: "a/b/c/d/:e", source: "src/realtime.ts:8", says: []string{"at most 4"}},
		{name: "no subscribe", channels: []*resourcesv1.RealtimeChannel{noSubscribe}, pattern: "orders/:orderId", source: "src/realtime.ts:8", says: []string{"subscribe"}},
		{name: "no publish", channels: []*resourcesv1.RealtimeChannel{noPublish}, pattern: "orders/:orderId", source: "src/realtime.ts:8", says: []string{"publish"}},
		{name: "a subscribe no channel knows", channels: []*resourcesv1.RealtimeChannel{unknownSubscribe}, pattern: "orders/:orderId", source: "src/realtime.ts:8", says: []string{"declares subscribe 7"}},
		{name: "a publish no channel knows", channels: []*resourcesv1.RealtimeChannel{unknownPublish}, pattern: "orders/:orderId", source: "src/realtime.ts:8", says: []string{"declares publish 7"}},
		{name: "a wildcard with no params", channels: []*resourcesv1.RealtimeChannel{wildcard}, pattern: "status/live", source: "src/realtime.ts:8", says: []string{"no param"}},
		{
			name:     "overlapping patterns",
			channels: []*resourcesv1.RealtimeChannel{ruleChannel("orders/:orderId", "src/realtime.ts:8"), ruleChannel("orders/latest", "src/realtime.ts:12")},
			pattern:  "orders/latest", source: "src/realtime.ts:12", says: []string{"overlaps", `"orders/:orderId"`, "src/realtime.ts:8"},
		},
		{
			name:     "overlapping patterns, the first with no source",
			channels: []*resourcesv1.RealtimeChannel{ruleChannel("orders/:orderId", ""), ruleChannel("orders/latest", "")},
			pattern:  "orders/latest", says: []string{"overlaps", "<unknown source>"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := RefuseConfig("app", &resourcesv1.RealtimeConfig{Channels: tc.channels})
			var refused *InvalidChannelError
			if !errors.As(err, &refused) || refused.Pattern != tc.pattern || refused.Source != tc.source {
				t.Fatalf("RefuseConfig() = %#v, want a InvalidChannelError for %q at %q", err, tc.pattern, tc.source)
			}
			for _, said := range append(tc.says, `channel "`+tc.pattern+`"`) {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("RefuseConfig() = %q, want it to say %q", err, said)
				}
			}
		})
	}
}

func TestAConfigWhoseNameOrTTLNoChannelRunsWithIsRefused(t *testing.T) {
	t.Parallel()

	if err := RefuseConfig(strings.Repeat("a", 51), &resourcesv1.RealtimeConfig{}); err == nil || !strings.Contains(err.Error(), "50") {
		t.Errorf("RefuseConfig(51-character name) = %v, want it refused naming the 50-character limit", err)
	}
	for _, ttl := range []time.Duration{0, -time.Second, 5 * time.Second, 10 * time.Minute} {
		if err := RefuseConfig("app", &resourcesv1.RealtimeConfig{TokenTtl: durationpb.New(ttl)}); err == nil || !strings.Contains(err.Error(), "token ttl") {
			t.Errorf("RefuseConfig(token ttl %s) = %v, want it refused", ttl, err)
		}
	}
}
