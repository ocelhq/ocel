package aws

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Table(ctx context.Context, class edge.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, class edge.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsTable, nil
}
