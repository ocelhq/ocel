package aws

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) WarmFunctions(ctx context.Context, targets []string, progress edge.Progress) error {
	return p.stacks.Warm(ctx, targets, progress)
}
