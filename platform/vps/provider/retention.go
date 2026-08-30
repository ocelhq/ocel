package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) ReconcileImages(ctx context.Context, _ providerkit.StackRef, app, coordinate string, report providerkit.Reporter) error {
	return p.host.Reconcile(ctx, app, coordinate, report)
}

func (p *Provider) ForgetReleases(ctx context.Context, ref providerkit.StackRef, app string, _ providerkit.Reporter) error {
	return p.host.Forget(ctx, ref.Class, app)
}

func (p *Provider) EnsureRelease(ctx context.Context, ref providerkit.StackRef, app, coordinate string, report providerkit.Reporter) error {
	held, err := p.host.HoldsImage(ctx, coordinate)
	if err != nil {
		return err
	}
	if !held {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s no longer holds %s, which %s was released from: the box keeps the last few releases of an app and this one has fallen out of that window, so there is nothing here to re-point at: deploy again",
			p.options.SSH.session().Destination(), coordinate, app)
	}
	physical := host.ContainerName(ref.Name.String(), app, "", coordinate)
	if report != nil {
		report.Say("Standing " + app + " back up as " + physical + " from the image this host already holds")
	}
	if err := p.host.StandUp(ctx, host.Container{Name: physical, App: app, Image: coordinate}); err != nil {
		return err
	}
	return p.host.Promote(ctx, ref.Class, app, coordinate)
}

var _ resources.ImageRetention = (*Provider)(nil)
