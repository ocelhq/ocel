package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) InspectStack(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.releases.Inspect(ctx, ref)
}
