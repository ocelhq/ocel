package control

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
		AWS:          func(context.Context) (aws.Config, error) { return p.Config, nil },
		Certificates: p.Options.Certificates,
		Namespace:    p.Namespace,
		EnsurePart:   p.ensurePartNamed,
		Cloudflare:   p.cloudflareOrigin(),
		StateBucket: func(ctx context.Context, tier environment.Tier) (string, error) {
			deployed, err := p.ReadDeployed(ctx, tier)
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

func (p *Provider) cloudflareOrigin() cloudflare.Origin {
	return cloudflare.Origin{Scope: p.cloudflareScope, Records: p.KeyValues()}
}

func (p *Provider) cloudflareScope(ctx context.Context) (cloudflare.OriginScope, error) {
	accountID, err := p.ReadAccountID(ctx)
	if err != nil {
		return cloudflare.OriginScope{}, err
	}
	return cloudflare.OriginScope{Vendor: string(Vendor), ID: accountID + "/" + p.Config.Region}, nil
}
