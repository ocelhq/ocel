package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (x *Proxy) DomainOwner(ctx context.Context, hostname string) (string, error) {
	accountID, err := requireAccountID("read what Cloudflare forwards")
	if err != nil {
		return "", err
	}
	zoneID, _, err := x.p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return "", err
	}
	return x.p.readForwardedOwner(ctx, zoneID, hostname)
}

func (x *Proxy) ProjectOwner(ctx context.Context, slug string, tier environment.Tier) (string, error) {
	return x.p.forwardingOwner(ctx, slug, tier)
}
