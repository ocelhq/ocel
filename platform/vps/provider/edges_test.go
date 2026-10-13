package vps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestABoxRunsTheTunnelTheCloudflareProxyReachesItThrough(t *testing.T) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) {
			return nil, errors.New("no box is reached to read what it runs")
		},
	)
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Facts().RunsTunnels || front.Hooks().Tunnels == nil || !host.CanRunTunnelTo(cloudflare.Kind) {
		t.Errorf("Facts().RunsTunnels = %v with the Cloudflare proxy's tunnels %v and a connector on the box %v, want a box reachable through the tunnel the proxy opens",
			p.Facts().RunsTunnels, front.Hooks().Tunnels, host.CanRunTunnelTo(cloudflare.Kind))
	}
}

func TestTheCloudflareProxyOpenedWithTunnelSetReachesTheBoxThroughATunnel(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	tunneled, err := p.Edges().Open(cloudflare.Kind, provider.Options{"tunnel": true})
	if err != nil {
		t.Fatal(err)
	}
	addressed, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !tunneled.Facts().TunnelsToOrigin || addressed.Facts().TunnelsToOrigin {
		t.Errorf("the proxy tunnels to the box %v with tunnel set and %v without, want only the one set to", tunneled.Facts().TunnelsToOrigin, addressed.Facts().TunnelsToOrigin)
	}
}

func TestTheCloudflareProxyInFrontOfABoxOwnsWhatItForwardsInOcelsNamespace(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%q): %v", cloudflare.Kind, err)
	}
	got := front.ProjectOwner("shop", environment.TierProduction)
	if want := cloudflare.NewProxy("staging", cloudflare.Options{}).ProjectOwner("shop", environment.TierProduction); got != want {
		t.Errorf("the proxy in front of a box owns shop's records as %q, want %q: it records ownership in the namespace ocel runs in, as it does in front of Google Cloud, not under the box's ssh destination", got, want)
	}
}

type gateRecordingMachine struct {
	boxedge.Machine
	gates map[string]*previewgate.Gate
	puts  int
	err   error
}

func (m *gateRecordingMachine) PutPreviewGate(_ context.Context, owner string, gate *previewgate.Gate) error {
	m.puts++
	if m.err != nil {
		return m.err
	}
	if gate == nil {
		delete(m.gates, owner)
		return nil
	}
	m.gates[owner] = gate
	return nil
}

func aGateRecordingMachine() *gateRecordingMachine {
	return &gateRecordingMachine{gates: map[string]*previewgate.Gate{}}
}

func TestReconcilingAPreviewStackThroughTheCloudflareProxyHandsTheSwitchboardTheGateOnTheSpec(t *testing.T) {
	t.Parallel()

	m := aGateRecordingMachine()
	surface := boxedge.Surface("shop", environment.TierPreview)
	gate := previewgate.Gate{Key: "recorded-key"}

	if _, err := vps.BoxProxyOver(m).Reconcile(context.Background(), edge.StackSpec{Tier: environment.TierPreview, Slug: "shop", PreviewGate: &gate}, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if got, held := m.gates[surface]; !held || got.Key != gate.Key {
		t.Errorf("after a protected deploy's edge step the switchboard holds preview gate %+v for %s, want %+v: a deploy that fails before it moves a pointer would leave previews already served ungated", got, surface, gate)
	}
}

func TestReconcilingAPreviewStackWithNoGateThroughTheCloudflareProxyTakesTheSwitchboardsGateOff(t *testing.T) {
	t.Parallel()

	m := aGateRecordingMachine()
	surface := boxedge.Surface("shop", environment.TierPreview)
	m.gates[surface] = &previewgate.Gate{Key: "earlier-key"}

	if _, err := vps.BoxProxyOver(m).Reconcile(context.Background(), edge.StackSpec{Tier: environment.TierPreview, Slug: "shop"}, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if got, held := m.gates[surface]; held {
		t.Errorf("after an open deploy's edge step the switchboard still holds preview gate %+v for %s, want it taken off", got, surface)
	}
}

func TestAProductionReconcileThroughTheCloudflareProxyNeverTouchesAPreviewGate(t *testing.T) {
	t.Parallel()

	m := aGateRecordingMachine()
	gate := previewgate.Gate{Key: "recorded-key"}

	if _, err := vps.BoxProxyOver(m).Reconcile(context.Background(), edge.StackSpec{Tier: environment.TierProduction, Slug: "shop", PreviewGate: &gate}, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if m.puts != 0 {
		t.Errorf("a production reconcile wrote the switchboard's preview gates %d times, want none", m.puts)
	}
}

func TestReconcilingAPreviewStackThroughTheCloudflareProxyFailsWhenTheSwitchboardRefusesTheGate(t *testing.T) {
	t.Parallel()

	m := aGateRecordingMachine()
	m.err = errors.New("switchboard unreachable")
	gate := previewgate.Gate{Key: "recorded-key"}

	_, err := vps.BoxProxyOver(m).Reconcile(context.Background(), edge.StackSpec{Tier: environment.TierPreview, Slug: "shop", PreviewGate: &gate}, edge.StackState{})
	if !errors.Is(err, m.err) {
		t.Fatalf("Reconcile() = %v, want it to fail with %v so the deploy restores the record as pending", err, m.err)
	}
}
