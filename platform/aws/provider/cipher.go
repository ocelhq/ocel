package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (p *Provider) Key(ctx context.Context, class edge.Class) (string, error) {
	deployed, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return deployed.VarsKeyARN, nil
}
