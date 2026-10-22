package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (x *Proxy) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	if err := x.p.recordBootstrapAccount(ctx, tier, accountID); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (x *Proxy) planBootstrap(ctx context.Context, tier environment.Tier) ([]edge.PlanChange, error) {
	_, err := x.p.bootstrapCredentials(ctx, tier, "plan the Cloudflare bootstrap")
	return nil, err
}

func (x *Proxy) planRemoveBootstrap(ctx context.Context, tier environment.Tier) ([]edge.PlanChange, error) {
	if _, err := x.p.requireTierAccount(ctx, tier, "plan removing the Cloudflare bootstrap"); err != nil {
		return nil, err
	}
	return x.p.planRemoveClientCertificates(ctx, tier)
}

func (x *Proxy) Teardown(ctx context.Context, tier environment.Tier) error {
	if _, err := x.p.requireTierAccount(ctx, tier, "tear the Cloudflare edge down"); err != nil {
		return err
	}
	if err := x.p.deleteClientCertificates(ctx, tier); err != nil {
		return err
	}
	return x.p.forgetBootstrapAccount(ctx, tier)
}
