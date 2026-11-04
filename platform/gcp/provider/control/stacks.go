package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

type stacks struct {
	provider.AbsentEngine
	p *Provider
}

func (s stacks) Resources(ctx context.Context, req provider.StackResources) ([]provider.PhysicalResource, error) {
	return resources.NewHookStacks(s.p.KeyValues(), resources.Hooks{ListResources: s.p.ListStackResources}).Resources(ctx, req)
}
