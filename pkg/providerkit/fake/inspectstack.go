package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func (p *Provider) InspectStack(_ context.Context, ref provider.StackRef) (provider.InspectedStack, error) {
	return p.stacks.Inspect(ref), nil
}
