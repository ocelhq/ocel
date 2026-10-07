package vps

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image", app.App)
	}
	physical := host.ContainerName(spec.Ref.Name.String(), app.App, app.BuildID, app.Image)
	store, err := p.storeSection(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("pin the store %s writes through: %w", app.App, err)
	}
	pinned := variables.Manifest{
		Slug:               spec.Ref.Project,
		Tier:               string(spec.Ref.Tier),
		Environment:        liveEnvironment(spec.Ref),
		Keys:               liveKeys(app.Values),
		Bindings:           liveBindings(app.Values),
		Store:              store,
		RealtimePublishURL: findRealtimePublishURL(app.Values.Bindings),
	}
	if hasQueue(app) {
		pinned.Queue = spec.Ref.Name.Env
	}
	manifest, err := variables.Render(pinned)
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	for _, owned := range []string{originguard.HealthPathVar, variables.EnvVar} {
		if _, taken := app.Values.ContainerEnv[owned]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %s sets %s, which ocel's runtime reserves: rename it", app.App, owned)
		}
	}
	if progress != nil {
		progress.Say("Starting " + app.App + "'s container " + physical)
	}
	healthPath, discovered := app.HealthCheckPath, ""
	if healthPath == "" {
		healthPath, discovered = app.DiscoveredHealthCheckPath, app.DiscoveredHealthCheckPath
	}
	if err := p.host.RunContainer(ctx, host.Container{
		Name: physical, Project: spec.Ref.Project, App: app.App, Image: app.Image,
		Tier: spec.Ref.Tier, Env: app.Values.ContainerEnv, HealthPath: healthPath, Manifest: manifest, Resolved: true,
	}); err != nil {
		return nil, err
	}
	target := physical + ":" + containerimage.PortText
	switch {
	case healthPath == "":
		if discovered, err = p.host.FindHealthPath(ctx, host.HealthPathSearch{App: app.App, Target: target, Window: host.DeployWindow}, progress); err != nil {
			return nil, err
		}
	case discovered != "" && progress != nil:
		progress.Say(app.App + " answers its health check on " + discovered + ", found by probing on an earlier release")
	}
	if err := p.host.Promote(ctx, spec.Ref.Tier, spec.Ref.Project, app.App, app.Image); err != nil {
		return nil, err
	}
	if err := p.runWorkers(ctx, spec, manifest, progress); err != nil {
		return nil, err
	}
	return []provider.AppContainer{{
		Name:                      app.App,
		Physical:                  physical,
		URL:                       "http://" + target,
		Image:                     app.Image,
		DiscoveredHealthCheckPath: discovered,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, progress progress.Log) error {
	if err := p.removeWorkers(ctx, ref); err != nil {
		return err
	}
	for _, container := range containers {
		if container.Physical == "" {
			continue
		}
		if progress != nil {
			progress.Say("Removing " + container.Name + "'s container " + container.Physical)
		}
		if err := p.host.TakeDown(ctx, ref.Tier, container.Physical); err != nil {
			return err
		}
		if err := p.removeStoreAccount(ctx, ref, container.Name); err != nil {
			return err
		}
	}
	return nil
}

func liveEnvironment(ref provider.StackRef) string {
	if ref.Tier == environment.TierProduction {
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
		kind := provider.ProtoBindingType(binding.Type)
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
