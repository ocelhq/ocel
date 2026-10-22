package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (x *Proxy) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (x *Proxy) planRemoveBootstrap(ctx context.Context, tier environment.Tier) ([]edge.PlanChange, error) {
	return x.p.planRemoveClientCertificates(ctx, tier)
}

func (x *Proxy) Teardown(ctx context.Context, tier environment.Tier) error {
	return x.p.deleteClientCertificates(ctx, tier)
}
