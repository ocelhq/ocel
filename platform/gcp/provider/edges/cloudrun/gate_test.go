package cloudrun_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
)

type gateRecorder struct {
	mu        sync.Mutex
	published map[string]*gate.Gate
	removed   []string
	refuse    error
}

func tiered(tier environment.Tier, slug string) string { return string(tier) + "/" + slug }

func (g *gateRecorder) Publish(_ context.Context, tier environment.Tier, slug string, held *gate.Gate) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse != nil {
		return g.refuse
	}
	if g.published == nil {
		g.published = map[string]*gate.Gate{}
	}
	g.published[tiered(tier, slug)] = held
	return nil
}

func (g *gateRecorder) Remove(_ context.Context, tier environment.Tier, slug string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removed = append(g.removed, tiered(tier, slug))
	return nil
}

var (
	previewShop    = tiered(environment.TierPreview, "shop")
	productionShop = tiered(environment.TierProduction, "shop")
	held           = gate.Gate{Key: "k", PasswordHash: "m", BypassSecrets: []string{}, AllowOptions: []string{}}
)

func TestCloudRunWithNothingInFrontGatesAtTheOrigin(t *testing.T) {
	t.Parallel()

	facts := cloudrun.New(&pinRecorder{}, &gateRecorder{}).Facts()

	if !facts.Gates || !facts.GatesAtOrigin {
		t.Errorf("Facts() = %+v, want the origin to run the gate its services mount", facts)
	}
	if facts.RunsCode || facts.ShieldsOrigin {
		t.Errorf("Facts() = %+v, want no code and no shielding: nothing is in front of the service", facts)
	}
}

func TestPublishingAGateWritesItToTheSecretItsTiersServicesMount(t *testing.T) {
	t.Parallel()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			gates := &gateRecorder{}
			front := cloudrun.New(&pinRecorder{}, gates)

			err := front.Hooks().PublishGate(t.Context(), edge.GatePublication{
				State: edge.StackState{Slug: "shop", Tier: tier},
				Gate:  &held,
			})

			if err != nil || len(gates.published) != 1 || gates.published[tiered(tier, "shop")] == nil || gates.published[tiered(tier, "shop")].Key != "k" {
				t.Errorf("published = %+v, %v, want shop's %s gate and nothing else", gates.published, err, tier)
			}
		})
	}
}

func TestPublishingNoGateWithdrawsIt(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{published: map[string]*gate.Gate{previewShop: &held}}
	front := cloudrun.New(&pinRecorder{}, gates)

	err := front.Hooks().PublishGate(t.Context(), edge.GatePublication{
		State: edge.StackState{Slug: "shop", Tier: environment.TierPreview},
	})

	if published, found := gates.published[previewShop]; err != nil || !found || published != nil {
		t.Errorf("published = %+v, %v, want shop's gate withdrawn", gates.published, err)
	}
}

func TestAFailedPublicationIsReported(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{refuse: errors.New("secret manager is down")}
	front := cloudrun.New(&pinRecorder{}, gates)

	err := front.Hooks().PublishGate(t.Context(), edge.GatePublication{
		State: edge.StackState{Slug: "shop", Tier: environment.TierPreview},
		Gate:  &held,
	})

	if err == nil {
		t.Error("PublishGate = nil, want the failure: the recorded gate then stays pending")
	}
}

func TestADeployReconcilesTheGateOfItsProject(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{published: map[string]*gate.Gate{previewShop: &held}}
	front := cloudrun.New(&pinRecorder{}, gates)

	if _, err := front.Reconcile(t.Context(), edge.StackSpec{Slug: "shop", Tier: environment.TierPreview}, edge.StackState{}); err != nil {
		t.Fatal(err)
	}

	if gates.published[previewShop] != nil {
		t.Errorf("published = %+v, want the gate a deploy without one withdrawn: protection was turned off", gates.published[previewShop])
	}
}

func TestAProtectedProductionDeployWritesTheProductionGateAndLeavesThePreviewGateAlone(t *testing.T) {
	t.Parallel()
	preview := gate.Gate{Key: "preview-key", PasswordHash: "preview-hash"}
	gates := &gateRecorder{published: map[string]*gate.Gate{previewShop: &preview}}
	front := cloudrun.New(&pinRecorder{}, gates)

	if _, err := front.Reconcile(t.Context(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction, Gate: &held}, edge.StackState{}); err != nil {
		t.Fatal(err)
	}

	if got := gates.published[productionShop]; got == nil || got.Key != "k" {
		t.Errorf("the production gate = %+v, want the one this deploy carries", got)
	}
	if got := gates.published[previewShop]; got == nil || got.Key != "preview-key" {
		t.Errorf("the preview gate = %+v, want it untouched: a production deploy never writes the preview's credentials", got)
	}
}

func TestAnOpenProductionDeployWithdrawsOnlyTheProductionGate(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{published: map[string]*gate.Gate{previewShop: &held, productionShop: &held}}
	front := cloudrun.New(&pinRecorder{}, gates)

	if _, err := front.Reconcile(t.Context(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{}); err != nil {
		t.Fatal(err)
	}

	if published, found := gates.published[productionShop]; !found || published != nil {
		t.Errorf("the production gate = %+v, want it withdrawn: protection.production was turned off", published)
	}
	if gates.published[previewShop] == nil {
		t.Error("the preview gate was withdrawn by a production deploy, which opens previews that are still protected")
	}
}

func TestDestroyingAStackRemovesTheGateSecretOfItsProjectInItsTier(t *testing.T) {
	t.Parallel()
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			gates := &gateRecorder{}
			front := cloudrun.New(&pinRecorder{}, gates)
			stack, err := front.Open(edge.StackState{Slug: "shop", Tier: tier})
			if err != nil {
				t.Fatal(err)
			}

			if err := stack.Destroy(t.Context()); err != nil {
				t.Fatal(err)
			}

			if len(gates.removed) != 1 || gates.removed[0] != tiered(tier, "shop") {
				t.Errorf("removed = %v, want shop's %s gate secret alone", gates.removed, tier)
			}
		})
	}
}
