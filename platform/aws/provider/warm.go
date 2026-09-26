package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) WarmFunctions(ctx context.Context, targets []string, progress providerkit.Progress) error {
	return p.stacks.Warm(ctx, targets, progress)
}
