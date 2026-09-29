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

func TestTheCloudflareProxyInFrontOfABoxOwnsWhatItForwardsInOcelsNamespace(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	front, err := p.Edges().Open(cloudflare.Kind)
	if err != nil {
		t.Fatalf("Edges().Open(%q): %v", cloudflare.Kind, err)
	}
	got := front.ProjectOwner("shop", environment.TierProduction)
	if want := cloudflare.NewProxy("staging").ProjectOwner("shop", environment.TierProduction); got != want {
		t.Errorf("the proxy in front of a box owns shop's records as %q, want %q: it records ownership in the namespace ocel runs in, as it does in front of Google Cloud, not under the box's ssh destination", got, want)
	}
}
