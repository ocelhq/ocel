package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (x *Proxy) DomainOwner(ctx context.Context, hostname string) (string, error) {
	accountID, err := x.p.anyTierAccount(ctx, "read what Cloudflare forwards")
	if err != nil {
		return "", err
	}
	names, err := x.p.names(ctx)
	if err != nil {
		return "", err
	}
	zoneID, _, err := x.p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return "", err
	}
	forwarding, forwarded, err := x.p.readForwardingOwner(ctx, zoneID, hostname)
	if err != nil || !forwarded {
		return "", err
	}
	return domainOwner(forwarding, names.origin), nil
}

func (x *Proxy) ProjectOwner(ctx context.Context, slug string, tier environment.Tier) (string, error) {
	names, err := x.p.names(ctx)
	if err != nil {
		return "", err
	}
	owner, err := names.forwardingOwner(slug, tier)
	if err != nil {
		return "", err
	}
	return owner.String(), nil
}
