package vps

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) ProvisionContainers(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.AppContainer, error) {
	app := plan.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names no image, and a box runs what a registry coordinate names and nothing else", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s carries no health check path, and up means a 2xx on the path the wire named rather than on one this provider chose", app.App)
	}
	physical := host.ContainerName(plan.Ref.Name.String(), app.App, app.Deployment, app.Image)
	if report != nil {
		report.Say("Standing " + app.App + " up as " + physical)
	}
	if err := p.host.StandUp(ctx, host.Container{
		Name: physical, App: app.App, Image: app.Image,
		Class: plan.Ref.Class, Env: app.Values.Delivered, Resolved: true,
	}); err != nil {
		return nil, err
	}
	if err := p.host.Promote(ctx, plan.Ref.Class, app.App, app.Image); err != nil {
		return nil, err
	}
	return []providerkit.AppContainer{{
		Name:     app.App,
		Physical: physical,
		URL:      "http://" + physical + ":" + host.AppPort,
		Image:    app.Image,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, report providerkit.Reporter) error {
	for _, container := range containers {
		if container.Physical == "" {
			continue
		}
		if report != nil {
			report.Say("Taking " + container.Physical + " down")
		}
		if err := p.host.TakeDown(ctx, container.Physical); err != nil {
			return err
		}
	}
	return nil
}

var _ resources.AppContainers = (*Provider)(nil)
