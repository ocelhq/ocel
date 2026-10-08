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
		case !p.removeFromRegistry(ctx, ref, app, image, images, log):
			continue
		}
		settled = append(settled, image)
	}
	if err := p.host.Settle(ctx, ref.Project, app, settled); err != nil && log != nil {
		log.Warn(fmt.Sprintf("Removed %s, and the box could not note it, so the next removal asks for it again: %v", strings.Join(settled, ", "), err))
	}
	return nil
}

type restoring interface {
	restore(ctx context.Context, imageRef string) (bool, error)
}

func (p *Provider) removeFromRegistry(ctx context.Context, ref provider.StackRef, app, image string, images provider.ImageStore, log progress.Log) bool {
	warn := func(format string, args ...any) {
		if log != nil {
			log.Warn(fmt.Sprintf(format, args...))
		}
	}
	claimed, err := p.host.IsClaimed(ctx, ref.Project, app, image)
	if err != nil {
		warn("Left %s in the registry it was pushed to, as whether a release claimed it again could not be read, and the next removal tries again: %v", image, err)
		return false
	}
	if claimed {
		return false
	}
	if err := images.Remove(ctx, image); err != nil {
		warn("Left %s in the registry it was pushed to, and the next removal tries again: %v", image, err)
		return false
	}
	registry := registryOf(image)
	claimed, err = p.host.IsClaimed(ctx, ref.Project, app, image)
	if err != nil {
		warn("Removed %s from %s, and whether a release claimed it meanwhile could not be read; a deploy that did finds it missing after provisioning and pushes it again: %v", image, registry, err)
		return true
	}
	if !claimed {
		if log != nil {
			log.Say("Removed " + app + "'s unused image " + image + " from " + registry)
		}
		return true
	}
	restorer, can := images.(restoring)
	if !can {
		warn("Removed %s from %s while a release claimed it, and nothing here can push it back, so a box that pulls it fails until a deploy pushes it again", image, registry)
		return false
	}
	held, err := restorer.restore(ctx, image)
	switch {
	case err != nil:
		warn("Removed %s from %s while a release claimed it, and pushing it back from %s failed, so a box that pulls it fails until a deploy pushes it again: %v", image, registry, images.Destination(), err)
	case !held:
		warn("Removed %s from %s while a release claimed it, and neither %s nor %s holds it now, so a box that pulls it fails until a deploy pushes it again", image, registry, registry, images.Destination())
	default:
		warn("Removed %s from %s while a release claimed it, so %s pushed it back", image, registry, images.Destination())
	}
	return false
}

func namesRegistry(imageRef string) bool {
	first, _, nested := strings.Cut(imageRef, "/")
	return nested && (strings.ContainsAny(first, ".:") || first == "localhost")
}

func registryOf(imageRef string) string {
	first, _, _ := strings.Cut(imageRef, "/")
	return first
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	return p.host.Forget(ctx, ref.Tier, ref.Project, app)
}
