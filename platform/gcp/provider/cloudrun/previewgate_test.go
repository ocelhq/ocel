package cloudrun_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
)

type gateRecorder struct {
	mu        sync.Mutex
	published map[string]*previewgate.Gate
	removed   []string
	refuse    error
}

func (g *gateRecorder) Publish(_ context.Context, slug string, gate *previewgate.Gate) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse != nil {
		return g.refuse
	}
	if g.published == nil {
		g.published = map[string]*previewgate.Gate{}
	}
	g.published[slug] = gate
	return nil
}

func (g *gateRecorder) Remove(_ context.Context, slug string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removed = append(g.removed, slug)
	return nil
}

var gate = previewgate.Gate{Key: "k", PasswordMAC: "m", BypassSecrets: []string{}, AllowOptions: []string{}}

func TestCloudRunWithNothingInFrontGatesPreviewsAtTheOrigin(t *testing.T) {
	t.Parallel()

	facts := cloudrun.New(&pinRecorder{}, &gateRecorder{}).Facts()

	if !facts.GatesPreviews || !facts.GatesPreviewsAtOrigin {
		t.Errorf("Facts() = %+v, want the origin to run the preview gate its services mount", facts)
	}
	if facts.RunsCode || facts.ShieldsOrigin {
		t.Errorf("Facts() = %+v, want no code and no shielding: nothing is in front of the service", facts)
	}
}

func TestPublishingAPreviewGateWritesItToTheSecretItsServicesMount(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{}
	front := cloudrun.New(&pinRecorder{}, gates)

	err := front.Hooks().PublishPreviewGate(t.Context(), edge.PreviewGatePublication{
		State: edge.StackState{Slug: "shop", Tier: environment.TierPreview},
		Gate:  &gate,
	})

	if err != nil || gates.published["shop"] == nil || gates.published["shop"].Key != "k" {
		t.Errorf("published = %+v, %v, want shop's gate", gates.published, err)
	}
}

func TestPublishingNoGateWithdrawsIt(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{published: map[string]*previewgate.Gate{"shop": &gate}}
	front := cloudrun.New(&pinRecorder{}, gates)

	err := front.Hooks().PublishPreviewGate(t.Context(), edge.PreviewGatePublication{
		State: edge.StackState{Slug: "shop", Tier: environment.TierPreview},
	})

	if _, held := gates.published["shop"]; err != nil || !held || gates.published["shop"] != nil {
		t.Errorf("published = %+v, %v, want shop's gate withdrawn", gates.published, err)
	}
}

func TestAFailedPublicationIsReported(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{refuse: errors.New("secret manager is down")}
	front := cloudrun.New(&pinRecorder{}, gates)

	err := front.Hooks().PublishPreviewGate(t.Context(), edge.PreviewGatePublication{
		State: edge.StackState{Slug: "shop", Tier: environment.TierPreview},
		Gate:  &gate,
	})

	if err == nil {
		t.Error("PublishPreviewGate = nil, want the failure: the recorded gate then stays pending")
	}
}

func TestADeployReconcilesThePreviewGateOfItsProject(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{published: map[string]*previewgate.Gate{"shop": &gate}}
	front := cloudrun.New(&pinRecorder{}, gates)

	if _, err := front.Reconcile(t.Context(), edge.StackSpec{Slug: "shop", Tier: environment.TierPreview}, edge.StackState{}); err != nil {
		t.Fatal(err)
	}

	if gates.published["shop"] != nil {
		t.Errorf("published = %+v, want the gate a deploy without one withdrawn: protection was turned off", gates.published["shop"])
	}
}

func TestAProductionDeployTouchesNoPreviewGate(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{}
	front := cloudrun.New(&pinRecorder{}, gates)

	if _, err := front.Reconcile(t.Context(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction, PreviewGate: &gate}, edge.StackState{}); err != nil {
		t.Fatal(err)
	}

	if len(gates.published) != 0 {
		t.Errorf("published = %+v, want none: production serves no preview", gates.published)
	}
}

func TestDestroyingAPreviewStackRemovesTheGateSecretOfItsProject(t *testing.T) {
	t.Parallel()
	gates := &gateRecorder{}
	front := cloudrun.New(&pinRecorder{}, gates)
	stack, err := front.Open(edge.StackState{Slug: "shop", Tier: environment.TierPreview})
	if err != nil {
		t.Fatal(err)
	}

	if err := stack.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(gates.removed) != 1 || gates.removed[0] != "shop" {
		t.Errorf("removed = %v, want shop's gate secret", gates.removed)
	}
}
