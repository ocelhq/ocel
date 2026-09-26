package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func (p *Provider) InspectStack(ctx context.Context, ref provider.StackRef) (provider.InspectedStack, error) {
	return p.stacks.Inspect(ctx, ref)
}
