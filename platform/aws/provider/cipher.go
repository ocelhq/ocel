package aws

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Key(ctx context.Context, class edge.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsKeyARN, nil
}
