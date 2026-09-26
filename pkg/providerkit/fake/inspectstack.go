package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func (p *Provider) InspectStack(_ context.Context, ref provider.StackRef) (provider.StackState, error) {
	return p.stacks.State(ref), nil
}
