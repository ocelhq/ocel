package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (p *Provider) ProvisionContainers(ctx context.Context, plan providerkit.StackPlan, progress edge.Progress) ([]providerkit.AppContainer, error) {
	app := plan.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s has no health check path", app.App)
	}
	physical := host.ContainerName(plan.Ref.Name.String(), app.App, app.Deployment, app.Image)
	store, err := p.storeSection(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("pin the store %s writes through: %w", app.App, err)
	}
	manifest, err := vars.Render(vars.Manifest{
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
	for _, owned := range []string{originguard.HealthPathVar, vars.EnvVar} {
		if _, taken := app.Values.Delivered[owned]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
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
		URL:      "http://" + physical + ":" + appbuild.InjectedPortText,
		Image:    app.Image,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, ref providerkit.StackRef, containers []providerkit.AppContainer, progress edge.Progress) error {
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
	if ref.Class == edge.ClassProduction {
		return ""
	}
	return ref.Name.Env
}

func liveKeys(held providerkit.AppValues) []live.Key {
	keys := make([]live.Key, 0, len(held.Secrets))
	for _, secret := range held.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func liveBindings(held providerkit.AppValues) []live.Binding {
	bindings := make([]live.Binding, 0, len(held.Bindings))
	for _, binding := range held.Bindings {
		kind := providerkit.WireBindingType(binding.Type)
		resource := binding.Resource
		if resource == "" {
			resource = binding.Name
		}
		bindings = append(bindings, live.Binding{
			Name:    binding.Name,
			Key:     naming.ResourceEnvName(kind, resource),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	return bindings
}
