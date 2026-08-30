package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) Release(ctx context.Context, rel host.Release, report providerkit.Reporter) error {
	return p.host.Release(ctx, rel, report)
}
