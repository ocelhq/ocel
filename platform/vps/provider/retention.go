package vps

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, images provider.ImageStore, log progress.Log) error {
	removed, err := p.host.Reconcile(ctx, ref.Project, app, imageRef, log)
	if err != nil {
		return err
	}
	if images == nil {
		return nil
	}
	for _, image := range removed {
		if err := images.Remove(ctx, image); err != nil && log != nil {
			log.Warn(fmt.Sprintf("Left %s in the registry it was pushed to: %v", image, err))
		}
	}
	return nil
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ progress.Log) error {
	return p.host.Forget(ctx, ref.Tier, ref.Project, app)
}
