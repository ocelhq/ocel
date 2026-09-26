package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) PackApp(ctx context.Context, req provider.PackAppRequest, progress edge.Progress) (provider.PackAppResult, error) {
	return p.stacks.PackApp(ctx, req, progress)
}
