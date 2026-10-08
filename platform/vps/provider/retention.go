package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, images provider.ImageStore, log progress.Log) error {
	removed, err := p.host.Reconcile(ctx, ref.Project, app, imageRef)
	if err != nil {
		return err
	}
	resources.SayRemovedImages(log, app, removed)
	if images != nil {
		resources.RemovePushedImages(ctx, images, app, removed, nil, log)
	}
	return nil
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	return p.host.Forget(ctx, ref.Tier, ref.Project, app)
}
