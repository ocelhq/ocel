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
	swept, err := p.host.Reconcile(ctx, ref.Project, app, imageRef)
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
			if err := images.Remove(ctx, image); err != nil {
				if log != nil {
					log.Warn(fmt.Sprintf("Left %s in the registry it was pushed to, and the next removal tries again: %v", image, err))
				}
				continue
			}
			if log != nil {
				log.Say("Removed " + app + "'s unused image " + image + " from " + images.Destination())
			}
		}
		settled = append(settled, image)
	}
	if err := p.host.Settle(ctx, ref.Project, app, settled); err != nil && log != nil {
		log.Warn(fmt.Sprintf("Removed %s, and the box could not note it, so the next removal asks for it again: %v", strings.Join(settled, ", "), err))
	}
	return nil
}

func namesRegistry(imageRef string) bool {
	first, _, nested := strings.Cut(imageRef, "/")
	return nested && (strings.ContainsAny(first, ".:") || first == "localhost")
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	return p.host.Forget(ctx, ref.Tier, ref.Project, app)
}
