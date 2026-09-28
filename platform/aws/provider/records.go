package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (p *Provider) Table(ctx context.Context, class edge.Class) (string, error) {
	deployed, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return deployed.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, class edge.Class) (string, error) {
	deployed, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return deployed.VarsTable, nil
}
