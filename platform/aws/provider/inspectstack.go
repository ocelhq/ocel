package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) InspectStack(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.stacks.Inspect(ctx, ref)
}
