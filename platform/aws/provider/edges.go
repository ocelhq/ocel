package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) edges() edges.Registry {
	return edges.Registry{Deps: p.edgeDeps(), Opened: &p.opened}
}

func (p *Provider) edgeDeps() edges.Deps {
	return edges.Deps{
		AWS:              func(context.Context) (aws.Config, error) { return p.aws, nil },
		Certificates:     p.options.Certificates,
		Namespace:        p.namespace,
		EnsurePart:       p.ensurePartNamed,
		CloudflareOrigin: p.cloudflareOrigin,
		KeyValues:        p.KeyValues(),
		StateBucket: func(ctx context.Context, tier environment.Tier) (string, error) {
			deployed, err := p.bootstrapped(ctx, tier)
			if err != nil {
				return "", err
			}
			if !deployed.Present {
				return "", refusal.Refuse(refusal.CodeNotReady, "the %s tier is not bootstrapped: run `%s`", tier, provider.BootstrapCommand(tier))
			}
			return deployed.Buckets[naming.StorageState], nil
		},
	}
}

func (p *Provider) cloudflareOrigin(ctx context.Context) (cloudflare.OriginScope, error) {
	accountID, err := p.accountID(ctx)
	if err != nil {
		return cloudflare.OriginScope{}, err
	}
	return cloudflare.OriginScope{Vendor: string(Vendor), ID: accountID + "/" + p.aws.Region}, nil
}
