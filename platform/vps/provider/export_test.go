package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var ProviderOver = newProvider

var MintStoreSecret = mintStoreSecret

func StoreCoordinate(ref provider.StackRef) records.SealScope { return storeCoordinate(ref) }

func StoreName(ref provider.StackRef) string { return storeName(ref) }

func Whoami(ctx context.Context, live hostSurvey) (provider.Principal, error) {
	return whoami(ctx, live)
}

func Elevating(inner provider.Bootstrap, gate func(context.Context) error) provider.Bootstrap {
	return elevating{Bootstrap: inner, elevated: gate}
}

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Recording(records records.Store) { p.records = records }

func (p *Provider) Resolving(look Lookup) { p.resolve = look }

func (p *Provider) Reaching(dial Reach) { p.reaches = dial }

func (p *Provider) Transforming(pass transformkit.Pass) { p.transform = pass }

func DNSVerdict(ctx context.Context, look Lookup, hostname, address string) provider.HostCheck {
	here, unread := look(ctx, address)
	return dnsVerdict(ctx, look, hostname, address, here, unread)
}

func DNSVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []provider.HostCheck {
	return dnsVerdicts(ctx, look, hostnames, address)
}

func ReachVerdict(ctx context.Context, dial Reach, address string) provider.HostCheck {
	return reachVerdict(ctx, dial, address)
}

func (p *Provider) Fronted() *Proxy { return p.options.Proxy }

func FrontOf(p *Proxy) host.Front { return p.front() }
