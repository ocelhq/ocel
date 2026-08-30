package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

func (p *Provider) ReconcileImages(ctx context.Context, _ providerkit.StackRef, app, coordinate string, report providerkit.Reporter) error {
	return p.host.Reconcile(ctx, app, coordinate, report)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref providerkit.StackRef, app string, _ providerkit.Reporter) error {
	return p.host.Forget(ctx, ref.Class, app)
}

var _ resources.ImageRetention = (*Provider)(nil)
