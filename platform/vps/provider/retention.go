package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, progress progress.Progress) error {
	return p.host.Reconcile(ctx, ref.Project, app, imageRef, progress)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ progress.Progress) error {
	return p.host.Forget(ctx, ref.Class, ref.Project, app)
}
