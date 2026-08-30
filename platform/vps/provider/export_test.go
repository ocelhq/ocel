package vps

import (
	"context"
	"net/http"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var ProviderOver = newProvider

func Whoami(ctx context.Context, live surveyor) (providerkit.Identity, error) {
	return whoami(ctx, live)
}

func Elevating(inner providerkit.Bootstrapper, gate func(context.Context) error) providerkit.Bootstrapper {
	return elevating{Bootstrapper: inner, elevated: gate}
}

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Probing(client *http.Client) { p.probing = client }
