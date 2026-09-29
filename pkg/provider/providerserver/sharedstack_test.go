package providerserver_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestADeployFlipsItsPromotionThroughTheRouterItsEdgePairsWith(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	routed := p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Builds("shop", environment.TierProduction, router.DefaultPointer)
	if routed["web"] == "" {
		t.Errorf("the relay router routes %v on %s after the deploy, want web's build: the promotion is flipped through the router its edge pairs with", routed, router.DefaultPointer)
	}
}

func TestADeployThroughAnEdgeThatCachesWholeResponsesPurgesTheHostnamesItFlipped(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if purged := relay.Purged(); len(purged) != 1 || !slices.Equal(purged[0], []string{"shop.example"}) {
		t.Errorf("the edge purged %v, want shop.example purged once after the flip: an edge that caches whole responses keeps serving the release a promote replaced until its cache of that hostname is dropped", purged)
	}
}

var errPurge = errors.New("the cache refused the purge")

func TestADeployWhosePurgeFailsStillServesWhatItFlippedAndSaysSo(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.RefusePurges(errPurge)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: the release is flipped, and a cache that outlives it is a warning", result.GetError())
	}
	if !slices.ContainsFunc(warnings(events), func(warning string) bool { return strings.Contains(warning, errPurge.Error()) }) {
		t.Errorf("the deploy warned %v, want the purge that failed named", warnings(events))
	}
}
