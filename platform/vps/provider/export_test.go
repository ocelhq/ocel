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

func (p *Provider) Resolving(look Lookup) { p.resolve = look }

func (p *Provider) Reaching(dial Reach) { p.reaches = dial }

func DNSVerdict(ctx context.Context, look Lookup, hostname, address string) providerkit.StandingCheck {
	here, unread := look(ctx, address)
	return dnsVerdict(ctx, look, hostname, address, here, unread)
}

func DNSVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []providerkit.StandingCheck {
	return dnsVerdicts(ctx, look, hostnames, address)
}

func ReachVerdict(ctx context.Context, dial Reach, address string) providerkit.StandingCheck {
	return reachVerdict(ctx, dial, address)
}
