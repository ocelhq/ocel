package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref providerkit.StackRef, app, imageRef string, progress edge.Progress) error {
	return p.host.Reconcile(ctx, ref.Project, app, imageRef, progress)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref providerkit.StackRef, app string, _ edge.Progress) error {
	return p.host.Forget(ctx, ref.Class, ref.Project, app)
}
