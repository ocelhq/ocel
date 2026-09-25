package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref providerkit.StackRef, app, coordinate string, progress providerkit.Progress) error {
	return p.host.Reconcile(ctx, ref.Project, app, coordinate, progress)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref providerkit.StackRef, app string, _ providerkit.Progress) error {
	return p.host.Forget(ctx, ref.Class, ref.Project, app)
}
