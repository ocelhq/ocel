package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/runtimekit/front"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (p *Provider) ProvisionContainers(ctx context.Context, plan providerkit.StackPlan, progress providerkit.Progress) ([]providerkit.AppContainer, error) {
	app := plan.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names no image", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s has no health check path", app.App)
	}
	physical := host.ContainerName(plan.Ref.Name.String(), app.App, app.Deployment, app.Image)
	store, err := p.storeSection(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("pin the store %s writes through: %w", app.App, err)
	}
	manifest, err := live.Render(live.Manifest{
		Slug:        plan.Ref.Project,
		Class:       string(plan.Ref.Class),
		Environment: liveEnvironment(plan.Ref),
		Keys:        liveKeys(app.Values),
		Bindings:    liveBindings(app.Values),
		Store:       store,
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	for _, owned := range []string{front.HealthPathVar, live.EnvVar} {
		if _, taken := app.Values.Delivered[owned]; taken {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"app %s sets %s, which ocel's runtime reserves: rename it", app.App, owned)
		}
	}
	if progress != nil {
		progress.Say("Standing " + app.App + " up as " + physical)
	}
	if err := p.host.StandUp(ctx, host.Container{
		Name: physical, Project: plan.Ref.Project, App: app.App, Image: app.Image,
		Class: plan.Ref.Class, Env: app.Values.Delivered, HealthPath: app.HealthCheckPath, Manifest: manifest, Resolved: true,
	}); err != nil {
		return nil, err
	}
	if err := p.host.Promote(ctx, plan.Ref.Class, plan.Ref.Project, app.App, app.Image); err != nil {
		return nil, err
	}
	return []providerkit.AppContainer{{
		Name:     app.App,
		Physical: physical,
		URL:      "http://" + physical + ":" + providerkit.InjectedPortText,
		Image:    app.Image,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, ref providerkit.StackRef, containers []providerkit.AppContainer, progress providerkit.Progress) error {
	for _, container := range containers {
		if container.Physical == "" {
			continue
		}
		if progress != nil {
			progress.Say("Taking " + container.Physical + " down")
		}
		if err := p.host.TakeDown(ctx, ref.Class, container.Physical); err != nil {
			return err
		}
		if err := p.removeStoreAccount(ctx, ref, container.Name); err != nil {
			return err
		}
	}
	return nil
}

func liveEnvironment(ref providerkit.StackRef) string {
	if ref.Class == providerkit.ClassProduction {
		return ""
	}
	return ref.Name.Env
}

func liveKeys(held providerkit.AppValues) []rt.Key {
	keys := make([]rt.Key, 0, len(held.Secrets))
	for _, secret := range held.Secrets {
		keys = append(keys, rt.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func liveBindings(held providerkit.AppValues) []rt.Binding {
	bindings := make([]rt.Binding, 0, len(held.Bindings))
	for _, binding := range held.Bindings {
		kind := providerkit.WireBindingType(binding.Type)
		resource := binding.Resource
		if resource == "" {
			resource = binding.Name
		}
		bindings = append(bindings, rt.Binding{
			Name:    binding.Name,
			Key:     naming.ResourceEnvName(kind, resource),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	return bindings
}
