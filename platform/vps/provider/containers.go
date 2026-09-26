package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress edge.Progress) ([]provider.AppContainer, error) {
	app := spec.App
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
	physical := host.ContainerName(spec.Ref.Name.String(), app.App, app.Deployment, app.Image)
	store, err := p.storeSection(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("pin the store %s writes through: %w", app.App, err)
	}
	manifest, err := vars.Render(vars.Manifest{
		Slug:        spec.Ref.Project,
		Class:       string(spec.Ref.Class),
		Environment: liveEnvironment(spec.Ref),
		Keys:        liveKeys(app.Values),
		Bindings:    liveBindings(app.Values),
		Store:       store,
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	for _, owned := range []string{originguard.HealthPathVar, vars.EnvVar} {
		if _, taken := app.Values.ContainerEnv[owned]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %s sets %s, which ocel's runtime reserves: rename it", app.App, owned)
		}
	}
	if progress != nil {
		progress.Say("Starting " + app.App + " as " + physical)
	}
	if err := p.host.RunContainer(ctx, host.Container{
		Name: physical, Project: spec.Ref.Project, App: app.App, Image: app.Image,
		Class: spec.Ref.Class, Env: app.Values.ContainerEnv, HealthPath: app.HealthCheckPath, Manifest: manifest, Resolved: true,
	}); err != nil {
		return nil, err
	}
	if err := p.host.Promote(ctx, spec.Ref.Class, spec.Ref.Project, app.App, app.Image); err != nil {
		return nil, err
	}
	return []provider.AppContainer{{
		Name:     app.App,
		Physical: physical,
		URL:      "http://" + physical + ":" + appbuild.InjectedPortText,
		Image:    app.Image,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, progress edge.Progress) error {
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

func liveEnvironment(ref provider.StackRef) string {
	if ref.Class == edge.ClassProduction {
		return ""
	}
	return ref.Name.Env
}

func liveKeys(values provider.AppValues) []live.Key {
	keys := make([]live.Key, 0, len(values.Secrets))
	for _, secret := range values.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func liveBindings(values provider.AppValues) []live.Binding {
	bindings := make([]live.Binding, 0, len(values.Bindings))
	for _, binding := range values.Bindings {
		kind := provider.WireBindingType(binding.Type)
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
