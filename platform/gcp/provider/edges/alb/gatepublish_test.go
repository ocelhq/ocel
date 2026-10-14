package alb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

type recordOpens struct {
	seal.Cipher
	mu     sync.Mutex
	opened []string
}

func (c *recordOpens) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	c.mu.Lock()
	c.opened = append(c.opened, string(bound.Bytes()))
	c.mu.Unlock()
	return c.Cipher.Open(ctx, tier, bound, sealed)
}

func (c *recordOpens) openedRecordOf(slug string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Contains(c.opened, string(gateRecordData(environment.TierPreview, slug).Bytes()))
}

func TestADeployThatChangesNoGateTheConfirmedPluginRunsOpensNoOtherProjectsGateRecord(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	reconcileGated(t, balancer, "shop", &shopGate)
	reconcileGated(t, balancer, "blog", &blogGate)
	ups := len(w.ups)
	opens := &recordOpens{Cipher: balancer.deps.Cipher}
	balancer.deps.Cipher = opens

	reconcileGated(t, balancer, "shop", &shopGate)
	reconcileGated(t, balancer, "docs", nil)

	if opens.openedRecordOf("blog") {
		t.Errorf("a deploy of shop with its gate unchanged, and an open deploy of docs, opened blog's gate record: every deploy would open every project's record")
	}
	if len(w.ups) != ups {
		t.Errorf("the deploys ran %v, want no rollout of a plugin that already runs the recorded gates", w.ups[ups:])
	}
}

func TestPublishingAgainAfterAFailedRolloutRollsTheGateOutAndOnlyThenConfirmsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	state := reconcileGated(t, balancer, "shop", nil).State()
	publish := balancer.Hooks().PublishGate
	w.failUps(GateStack(environment.TierPreview), errors.New("the rollout failed"))

	if err := publish(ctx, edge.GatePublication{State: state, Gate: &shopGate}); err == nil {
		t.Fatal("PublishGate = nil, want the failed rollout")
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); strings.Contains(config, shopGate.Key) {
		t.Fatalf("the plugin runs %q after a failed rollout, want the old config", config)
	}

	if err := publish(ctx, edge.GatePublication{State: state, Gate: &shopGate}); err != nil {
		t.Fatalf("PublishGate (retry) = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Errorf("the plugin runs %q after the retry, want the gate the failed rollout never carried", config)
	}
	if recorded, found, err := balancer.readGatePublication(ctx, environment.TierPreview); err != nil || !found || !recorded.Confirmed {
		t.Errorf("publication = %+v, %t, %v, want it confirmed once the retry rolled out", recorded, found, err)
	}
}
