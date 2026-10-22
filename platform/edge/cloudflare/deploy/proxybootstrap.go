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
	if _, err := bootstrapCredentials(); err != nil {
		return nil, err
	}
	_, err := x.p.tierAccount(ctx, tier, "plan the Cloudflare bootstrap")
	return nil, err
}

func (x *Proxy) planRemoveBootstrap(ctx context.Context, tier environment.Tier) ([]edge.PlanChange, error) {
	accountID, err := x.p.tierAccount(ctx, tier, "plan removing the Cloudflare bootstrap")
	if err != nil {
		return nil, err
	}
	names, err := x.p.names(ctx)
	if err != nil {
		return nil, err
	}
	return x.p.planRemoveClientCertificates(ctx, accountID, certificateOwner{origin: names.origin, tier: tier})
}

func (x *Proxy) Teardown(ctx context.Context, tier environment.Tier) error {
	accountID, err := x.p.tierAccount(ctx, tier, "tear the Cloudflare edge down")
	if err != nil {
		return err
	}
	names, err := x.p.names(ctx)
	if err != nil {
		return err
	}
	if err := x.p.deleteClientCertificates(ctx, accountID, certificateOwner{origin: names.origin, tier: tier}); err != nil {
		return err
	}
	return x.p.forgetTierRecord(ctx, tier)
}
