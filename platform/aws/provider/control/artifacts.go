package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func (p *Provider) Buckets(ctx context.Context, tier environment.Tier) (naming.Buckets, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return nil, err
	}
	return deployed.Buckets, nil
}
