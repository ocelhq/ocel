package vps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
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
