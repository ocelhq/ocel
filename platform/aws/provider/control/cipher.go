package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *Provider) Key(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.VariablesKeyARN, nil
}
