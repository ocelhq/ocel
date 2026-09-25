package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) Table(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.StateTable, nil
}

func (p *Provider) ValuesTable(ctx context.Context, class providerkit.Class) (string, error) {
	held, err := p.bootstrapped(ctx, class)
	if err != nil {
		return "", err
	}
	return held.VarsTable, nil
}
