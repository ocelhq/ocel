package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *Provider) Table(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.VariablesTable, nil
}
