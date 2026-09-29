package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) PackApp(ctx context.Context, req provider.PackAppRequest, progress progress.Log) (provider.PackAppResult, error) {
	return p.stacks.PackApp(ctx, req, progress)
}
