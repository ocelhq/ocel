package alb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func TestPublishingAgainAfterAFailedRolloutRollsTheGateOutAndOnlyThenConfirmsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	state := reconcileGated(t, balancer, "shop", nil).State()
	publish := balancer.Hooks().PublishPreviewGate
	w.failUps(GateStack(environment.TierPreview), errors.New("the rollout failed"))

	if err := publish(ctx, edge.PreviewGatePublication{State: state, Gate: &shopGate}); err == nil {
		t.Fatal("PublishPreviewGate = nil, want the failed rollout")
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); strings.Contains(config, shopGate.Key) {
		t.Fatalf("the plugin runs %q after a failed rollout, want the old config", config)
	}

	if err := publish(ctx, edge.PreviewGatePublication{State: state, Gate: &shopGate}); err != nil {
		t.Fatalf("PublishPreviewGate (retry) = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Errorf("the plugin runs %q after the retry, want the gate the failed rollout never carried", config)
	}
	if recorded, found, err := balancer.readGatePublication(ctx); err != nil || !found || !recorded.Confirmed {
		t.Errorf("publication = %+v, %t, %v, want it confirmed once the retry rolled out", recorded, found, err)
	}
}
