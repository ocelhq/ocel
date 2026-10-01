package vps

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var ProviderOver = newProvider

func (p *Provider) Timing(now func() time.Time) { p.now = now }

func (p *Provider) Clock() func() time.Time { return p.now }

var MintStoreSecret = mintStoreSecret

var NewStoreSecretAssociatedData = newStoreSecretAssociatedData

var NewPostgresSecretAssociatedData = newPostgresSecretAssociatedData

func StoreName(ref provider.StackRef) string { return storeName(ref) }

func Whoami(ctx context.Context, live hostSurvey) (provider.Principal, error) {
	return whoami(ctx, live)
}

func Elevating(inner provider.Bootstrap, gate func(context.Context) error) provider.Bootstrap {
	return elevating{Bootstrap: inner, elevated: gate}
}

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Recording(store keyvalue.Store) { p.keyValues = store }

func (p *Provider) Resolving(look Lookup) { p.resolve = look }

func (p *Provider) Reaching(dial Reach) { p.reaches = dial }

func (p *Provider) Transforming(pass transform.Pass) { p.transform = pass }

func DNSVerdict(ctx context.Context, look Lookup, hostname, address string) provider.HostCheck {
	here, unread := look(ctx, address)
	return dnsVerdict(ctx, look, hostname, address, here, unread)
}

func ProxiedVerdict(ctx context.Context, look Lookup, serving provider.Liveness, hostname, address string) provider.HostCheck {
	return proxiedVerdict(ctx, look, serving, hostname, address)
}

func DNSVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []provider.HostCheck {
	return dnsVerdicts(ctx, look, hostnames, address)
}

func ReachVerdict(ctx context.Context, dial Reach, address string) provider.HostCheck {
	return reachVerdict(ctx, dial, address)
}

func (p *Provider) Fronted() *Proxy { return p.options.Proxy }

func FrontOf(p *Proxy) host.Front { return p.front() }
