package realtime

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func taggedRealtimeHarness(t *testing.T) (*realtimeHarness, *googletest.TierTags) {
	t.Helper()
	h := newRealtimeHarness(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.env.clients)
	h.pubsub.TierTags, h.secrets.TierTags = tags, tags
	return h, tags
}

func TestARealtimesGatewayTopicAndSecretsCarryTheirTiersTag(t *testing.T) {
	t.Parallel()
	h, tags := taggedRealtimeHarness(t)
	h.provision(t, "app")

	production := []string{tags.ValueOf(environment.TierProduction)}
	for _, parent := range []string{
		h.env.clients.RunServiceTagged(h.env.gateway).Name,
		googletest.PubSubTagParent(h.topic()),
		googletest.SecretTagParent(must(h.env.clients.RealtimeKeysSecret(environment.TierProduction, "shop", "prod"))),
		googletest.SecretTagParent(must(h.env.clients.RealtimeSigningSecret("shop", "prod", "app"))),
	} {
		if got := tags.Bound(parent); !slices.Equal(got, production) {
			t.Errorf("%s carries %v, want the production value", parent, got)
		}
	}
}

func TestRemovingTheLastRealtimeRemovesEachInlineTagBeforeItsTopicOrSecret(t *testing.T) {
	t.Parallel()
	h, _ := taggedRealtimeHarness(t)
	h.remove(t, h.provision(t, "app"))

	if len(h.pubsub.DeletedTagged) != 0 || len(h.secrets.DeletedTagged) != 0 {
		t.Errorf("%v and %v were deleted still carrying their tier tag, which Google then never lets go of", h.pubsub.DeletedTagged, h.secrets.DeletedTagged)
	}
}
