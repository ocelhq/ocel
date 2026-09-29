package providerserver_test

import (
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
