package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref providerkit.StackRef, app, imageRef string, progress providerkit.Progress) error {
	return p.host.Reconcile(ctx, ref.Project, app, imageRef, progress)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref providerkit.StackRef, app string, _ providerkit.Progress) error {
	return p.host.Forget(ctx, ref.Class, ref.Project, app)
}
