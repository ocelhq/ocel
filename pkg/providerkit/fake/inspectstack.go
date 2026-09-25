package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) InspectStack(_ context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	return p.releases.State(ref), nil
}
