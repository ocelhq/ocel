package manifest

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func realtimeResource(name, source string, ttl time.Duration, channels ...declaredChannel) declaredResource {
	config := &resourcesv1.RealtimeConfig{}
	if ttl != 0 {
		config.TokenTtl = durationpb.New(ttl)
	}
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: name, Realtime: config, RealtimeChannels: channels, Source: source}
}

func realtimeChannel(pattern, source string) declaredChannel {
	return declaredChannel{
		Pattern:   pattern,
		Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE,
		Publish:   resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER,
		Source:    source,
	}
}

func wildcardChannel(pattern, source string) declaredChannel {
	channel := realtimeChannel(pattern, source)
	channel.Wildcard = true
	return channel
}

func findRealtime(m *contractv1.Manifest, logical string) *resourcesv1.RealtimeConfig {
	for _, r := range m.GetResources() {
		if r.GetLogicalName() == logical {
			return r.GetRealtime()
		}
	}
	return nil
}

func TestARealtimeResourceReachesTheManifestWithItsChannels(t *testing.T) {
	t.Parallel()

	status := realtimeChannel("status", "src/realtime.ts:20")
	status.Subscribe = resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC
	rooms := realtimeChannel("rooms/:roomId", "src/realtime.ts:16")
	rooms.Publish = resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE
	rooms.Schema = `{"type":"object"}`
	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		realtimeResource("app", "src/realtime.ts:3", 90*time.Second,
			realtimeChannel("orders/:orderId", "src/realtime.ts:8"),
			wildcardChannel("projects/:projectId/deploys/:deployId", "src/realtime.ts:12"),
			rooms,
			status,
		),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	app := findRealtime(m, "realtime--app")
	if app == nil {
		t.Fatalf("manifest resources = %v, want realtime--app", m.GetResources())
	}
	if app.GetTokenTtl().AsDuration() != 90*time.Second {
		t.Errorf("token ttl = %v, want 90s as declared", app.GetTokenTtl())
	}
	channels := app.GetChannels()
	if len(channels) != 4 {
		t.Fatalf("channels = %v, want all four", channels)
	}
	if !channels[1].GetWildcard() || channels[1].GetSource() != "src/realtime.ts:12" {
		t.Errorf("channel 1 = %v, want the wildcard deploys pattern declared at src/realtime.ts:12", channels[1])
	}
	if channels[2].GetPublish() != resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE || channels[2].GetSchema() != `{"type":"object"}` {
		t.Errorf("channel 2 = %v, want rooms published by rule with its schema", channels[2])
	}
	if channels[3].GetSubscribe() != resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC {
		t.Errorf("channel 3 = %v, want status subscribed publicly", channels[3])
	}
}

func TestARealtimeResourceDeclaringNoTokenTTLReachesTheManifestWithSixtySeconds(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		realtimeResource("app", "src/realtime.ts:3", 0, realtimeChannel("orders/:orderId", "src/realtime.ts:8")),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	if ttl := findRealtime(m, "realtime--app").GetTokenTtl(); ttl == nil || ttl.AsDuration() != 60*time.Second {
		t.Errorf("token ttl = %v, want 60s filled in: the manifest carries the ttl every minter uses", ttl)
	}
}

func TestABadRealtimeDeclarationIsRefusedAtTheLineDeclaringIt(t *testing.T) {
	t.Parallel()

	noSubscribe := realtimeChannel("orders/:orderId", "src/realtime.ts:8")
	noSubscribe.Subscribe = resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_UNSPECIFIED
	noPublish := realtimeChannel("orders/:orderId", "src/realtime.ts:8")
	noPublish.Publish = resourcesv1.RealtimePublish_REALTIME_PUBLISH_UNSPECIFIED
	for _, tc := range []struct {
		name     string
		resource declaredResource
		at       string
		says     []string
	}{
		{
			name:     "a pattern of five segments",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, realtimeChannel("a/b/c/d/:e", "src/realtime.ts:8")),
			at:       "src/realtime.ts:8", says: []string{`"a/b/c/d/:e"`, "at most 4"},
		},
		{
			name:     "a malformed parameter",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, realtimeChannel("orders/:order-id", "src/realtime.ts:8")),
			at:       "src/realtime.ts:8", says: []string{`":order-id"`, "parameter"},
		},
		{
			name:     "an illegal literal",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, realtimeChannel("order_items/:id", "src/realtime.ts:8")),
			at:       "src/realtime.ts:8", says: []string{`"order_items"`, "literal"},
		},
		{
			name:     "a wildcard on a pattern with no params",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, wildcardChannel("status/live", "src/realtime.ts:8")),
			at:       "src/realtime.ts:8", says: []string{`"status/live"`, "wildcard", "no param"},
		},
		{
			name:     "a token ttl under ten seconds",
			resource: realtimeResource("app", "src/realtime.ts:3", 5*time.Second),
			at:       "src/realtime.ts:3", says: []string{`realtime "app"`, "5s", "10s"},
		},
		{
			name:     "a token ttl over five minutes",
			resource: realtimeResource("app", "src/realtime.ts:3", 10*time.Minute),
			at:       "src/realtime.ts:3", says: []string{`realtime "app"`, "10m0s", "5m0s"},
		},
		{
			name:     "a channel naming no subscribe",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, noSubscribe),
			at:       "src/realtime.ts:8", says: []string{`"orders/:orderId"`, "subscribe"},
		},
		{
			name:     "a channel naming no publish",
			resource: realtimeResource("app", "src/realtime.ts:3", 0, noPublish),
			at:       "src/realtime.ts:8", says: []string{`"orders/:orderId"`, "publish"},
		},
		{
			name:     "a name that is no resource name",
			resource: realtimeResource("App_Live", "src/realtime.ts:3", 0),
			at:       "src/realtime.ts:3", says: []string{`"App_Live"`, "lowercase"},
		},
		{
			name:     "a name too long to begin a channel",
			resource: realtimeResource(strings.Repeat("a", 51), "src/realtime.ts:3", 0),
			at:       "src/realtime.ts:3", says: []string{"50 characters"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{tc.resource}, nil)
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

func TestTwoOverlappingChannelPatternsAreRefusedNamingBothSites(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		realtimeResource("app", "src/realtime.ts:3", 0,
			realtimeChannel("orders/:orderId", "src/realtime.ts:8"),
			realtimeChannel("orders/latest", "src/realtime.ts:12"),
		),
	}, nil)
	var invalid *InvalidDeclarationError
	if !errors.As(err, &invalid) || invalid.Source != "src/realtime.ts:12" {
		t.Fatalf("assemble() = %v, want the overlap refused at src/realtime.ts:12", err)
	}
	for _, said := range []string{"src/realtime.ts:8", `"orders/:orderId"`, `"orders/latest"`, "overlap"} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("assemble() = %q, want it to say %q", err, said)
		}
	}
}

func TestAWildcardWhoseSubscriptionWouldReachAnotherPatternsChannelsIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		realtimeResource("app", "src/realtime.ts:3", 0,
			realtimeChannel("projects/:projectId/settings", "src/realtime.ts:8"),
			wildcardChannel("projects/:projectId/deploys/:deployId", "src/realtime.ts:12"),
		),
	}, nil)
	var invalid *InvalidDeclarationError
	if !errors.As(err, &invalid) || invalid.Source != "src/realtime.ts:12" {
		t.Fatalf("assemble() = %v, want the wildcard refused at src/realtime.ts:12", err)
	}
	for _, said := range []string{"src/realtime.ts:8", `"projects/:projectId/settings"`, "wildcard"} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("assemble() = %q, want it to say %q", err, said)
		}
	}
}

func TestPatternsOfTwoRealtimeResourcesAreTheirOwnChannels(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		realtimeResource("app", "src/app.ts:3", 0, wildcardChannel("orders/:orderId", "src/app.ts:4")),
		realtimeResource("admin", "src/admin.ts:3", 0, realtimeChannel("orders/:orderId", "src/admin.ts:4")),
	}, nil)
	if err != nil {
		t.Errorf("assemble() = %v, want two resources free to name the same pattern: each is its own namespace", err)
	}
}

func TestAChannelIsAttributedToItsOwnLineOrElseToItsResources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	declared := declaredResources(root, []declaration.Resource{{
		Type:   resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME,
		Name:   "app",
		Source: filepath.Join(root, "src", "realtime.go") + ":10",
		Realtime: &resourcesv1.RealtimeConfig{Channels: []*resourcesv1.RealtimeChannel{
			{Pattern: "orders/:orderId", Source: filepath.Join(root, "src", "orders.go") + ":22"},
			{Pattern: "status"},
		}},
	}})

	channels := declared[0].RealtimeChannels
	if len(channels) != 2 || channels[0].Source != "src/orders.go:22" || channels[1].Source != "src/realtime.go:10" {
		t.Errorf("channels = %+v, want orders at src/orders.go:22 and status at its resource's src/realtime.go:10", channels)
	}
}
