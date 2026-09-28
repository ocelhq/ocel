package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *Provider) Key(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return "", err
	}
	return deployed.VarsKeyARN, nil
}
