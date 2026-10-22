package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestABoxRunsTheTunnelTheCloudflareProxyReachesItThrough(t *testing.T) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) {
			return nil, errors.New("no box is reached to read what it runs")
		},
	)
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	if tunnels := front.Hooks().Tunnels; !p.Facts().RunsTunnels || tunnels == nil || tunnels.Connector.Image == "" {
		t.Errorf("Facts().RunsTunnels = %v with the Cloudflare proxy's tunnels %+v, want a box reachable through the tunnel the proxy opens, running the connector it names",
			p.Facts().RunsTunnels, tunnels)
	}
}

func TestABoxRefusesATunnelToAnEdgeThatOpensNone(t *testing.T) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	_, err := p.FindTunnelHooks(edge.None)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "Remove `tunnel`") {
		t.Errorf("FindTunnelHooks(none) = %v, want it refused invalid, naming the option to remove: an edge that opens no tunnel names no connector for the box to run", err)
	}
}

func TestTheCloudflareProxyOpenedWithTunnelSetReachesTheBoxThroughATunnel(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
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
		func(context.Context) (ssh.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%q): %v", cloudflare.Kind, err)
	}
	got, err := front.ProjectOwner(t.Context(), "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("ProjectOwner: %v", err)
	}
	want, err := cloudflare.NewProxy("staging", func(context.Context) (cloudflare.OriginScope, error) {
		return cloudflare.OriginScope{Vendor: "vps", ID: "k3j9x0"}, nil
	}, cloudflare.Options{}).ProjectOwner(t.Context(), "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("ProjectOwner: %v", err)
	}
	if got != want {
		t.Errorf("the proxy in front of a box owns shop's records as %q, want %q: it records ownership in the namespace ocel runs in, as it does in front of Google Cloud, not under the box's ssh destination", got, want)
	}
}
