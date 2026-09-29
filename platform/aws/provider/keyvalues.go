package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *Provider) Table(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.VariablesTable, nil
}
