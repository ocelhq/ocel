package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, images provider.ImageStore, log progress.Log) error {
	swept, err := p.images.Reconcile(ctx, ref.Project, app, imageRef)
	if err != nil {
		return err
	}
	resources.SayRemovedImages(log, app, swept.Removed)
	var settled []string
	for _, image := range swept.Unused {
		switch {
		case !namesRegistry(image):
		case images == nil:
			continue
		default:
			claimed, err := p.images.IsClaimed(ctx, ref.Project, app, image)
			if err != nil {
				log.Warn(fmt.Sprintf("Left %s in the registry: %v", image, err))
				continue
			}
			if claimed {
				continue
			}
			if err := images.Remove(ctx, image); err != nil {
				log.Warn(fmt.Sprintf("Left %s in the registry: %v", image, err))
				continue
			}
			log.Say("Removed " + app + "'s unused image " + image)
		}
		settled = append(settled, image)
	}
	if err := p.images.Settle(ctx, ref.Project, app, settled); err != nil {
		log.Warn(fmt.Sprintf("Removed %s but could not record it: %v", strings.Join(settled, ", "), err))
	}
	return nil
}

func namesRegistry(imageRef string) bool {
	first, _, nested := strings.Cut(imageRef, "/")
	return nested && (strings.ContainsAny(first, ".:") || first == "localhost")
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	return p.images.Forget(ctx, ref.Tier, ref.Project, ref.Name.Env, app)
}
