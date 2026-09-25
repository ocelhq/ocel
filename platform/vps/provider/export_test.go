package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var ProviderOver = newProvider

var MintStoreSecret = mintStoreSecret

func StoreCoordinate(ref providerkit.StackRef) providerkit.Coordinate { return storeCoordinate(ref) }

func StoreName(ref providerkit.StackRef) string { return storeName(ref) }

func Whoami(ctx context.Context, live surveyor) (providerkit.Identity, error) {
	return whoami(ctx, live)
}

func Elevating(inner providerkit.Bootstrap, gate func(context.Context) error) providerkit.Bootstrap {
	return elevating{Bootstrap: inner, elevated: gate}
}

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Recording(records providerkit.RecordStore) { p.records = records }

func (p *Provider) Resolving(look Lookup) { p.resolve = look }

func (p *Provider) Reaching(dial Reach) { p.reaches = dial }

func (p *Provider) Transforming(pass transformkit.Evaluator) { p.transform = pass }

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

func (p *Provider) Fronted() *Proxy { return p.options.Proxy }
