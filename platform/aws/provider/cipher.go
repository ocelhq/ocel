package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) Key(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsKeyARN, nil
}
