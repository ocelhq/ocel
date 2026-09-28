package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
)

func (p *Provider) WarmFunctions(ctx context.Context, targets []string, progress progress.Progress) error {
	return p.stacks.Warm(ctx, targets, progress)
}
