package box_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

func TestDestroyingAStackTakesTheProjectsNetworkAfterItsRoutes(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	forgot := slices.IndexFunc(stood.calls, func(call string) bool { return call == "forget network production/"+slug })
	if forgot < 0 {
		t.Fatalf("a torn-down project leaves its network on the box, holding one of the engine's subnets for nothing: %v", stood.calls)
	}
	if unrouted := slices.IndexFunc(stood.calls, func(call string) bool { return call == "unroute "+box.Surface(slug, edge.ClassProduction) }); unrouted > forgot {
		t.Errorf("the network was forgotten at %d and the surface unrouted at %d: %v", forgot, unrouted, stood.calls)
	}
}

func TestANetworkThatWillNotGoIsReportedAndTheRestOfTheTeardownStillRuns(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	refusal := errors.New("the engine is holding it")
	stood.refuseOn("ForgetNetwork", refusal)
	err := stack.Destroy(context.Background())
	if !errors.Is(err, refusal) {
		t.Fatalf("Destroy() = %v, want the network's refusal carried out", err)
	}
	if !slices.ContainsFunc(stood.calls, func(call string) bool { return call == "unroute "+box.Surface(slug, edge.ClassProduction) }) {
		t.Errorf("a network that would not go stopped the teardown before the routes were taken: %v", stood.calls)
	}
}
