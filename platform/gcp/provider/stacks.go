package gcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	vars "github.com/ocelhq/ocel/platform/gcp/provider/live"
)

func serviceFor(names Names, spec provider.StackSpec, app *provider.AppSpec, function string) (string, error) {
	if app.PreviewLabel != "" {
		return names.PreviewService(app.PreviewLabel, app.App, function)
	}
	return names.Service(spec.Ref.Project, spec.Ref.Name.Env, app.App, function)
}

const previewOpenWarning = "is a preview and answers anyone who knows its Cloud Run url: the %q edge shields nothing, " +
	"and Cloud Run's invoker check would shut browsers out too. Front previews with an edge that shields the origin, or keep their urls to yourselves"

func warnPreviewOpen(spec provider.StackSpec, service string, progress edge.Progress) {
	if spec.Ref.Class != edge.ClassPreview || factsOf(spec.Edge).ShieldsOrigin {
		return
	}
	kind := direct.Kind
	if spec.Edge != nil {
		kind = spec.Edge.Kind()
	}
	say(progress, service+" "+fmt.Sprintf(previewOpenWarning, kind))
}

func (p *Provider) ProvisionFunctions(ctx context.Context, spec provider.StackSpec, progress edge.Progress) ([]provider.Function, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	account := names.WorkloadAccountEmail(spec.Ref.Class)
	own, err := p.runtimeEnv(names, spec)
	if err != nil {
		return nil, err
	}
	deployed := make([]provider.Function, 0, len(app.Functions))
	for _, fn := range app.Functions {
		if err := runsX8664(fn.Framework.Arch, "function "+fn.Name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(fn.Image) == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"function %s names no image, and a function on Cloud Run is the container a registry coordinate names", fn.Name)
		}
		service, err := serviceFor(names, spec, app, fn.Name)
		if err != nil {
			return nil, err
		}
		values, err := mergedValues(fn.Name, app.Values.ContainerEnv, fn.Env)
		if err != nil {
			return nil, err
		}
		if values, err = mergedValues(fn.Name, values, own); err != nil {
			return nil, err
		}
		ran, err := p.deployService(ctx, serving{
			service: service,
			image:   fn.Image,
			env:     values,
			account: account,
			compute: provider.ComputeServerless,
			public:  true,
			ingress: ingressFor(factsOf(spec.Edge)),
			memory:  fn.Memory,
			timeout: fn.Timeout,
		}, progress)
		if err != nil {
			return nil, err
		}
		warnPreviewOpen(spec, service, progress)
		deployed = append(deployed, provider.Function{
			Name: fn.Name, Physical: service, URL: ran.url, Revision: ran.revision,
		})
	}
	return deployed, nil
}

func (p *Provider) RemoveFunctions(ctx context.Context, _ provider.StackRef, functions []provider.Function, progress edge.Progress) error {
	for _, function := range functions {
		if function.Physical == "" {
			continue
		}
		if err := p.tearDown(ctx, function.Physical, progress); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress edge.Progress) ([]provider.AppContainer, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image, and a Cloud Run service runs what a registry coordinate names and nothing else", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no health check path, and up means a 2xx on the path the wire named rather than on one this provider chose", app.App)
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	service, err := serviceFor(names, spec, app, app.App)
	if err != nil {
		return nil, err
	}
	own, err := p.runtimeEnv(names, spec)
	if err != nil {
		return nil, err
	}
	values, err := mergedValues(app.App, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	ran, err := p.deployService(ctx, serving{
		service: service,
		image:   app.Image,
		env:     values,
		account: names.WorkloadAccountEmail(spec.Ref.Class),
		compute: provider.ComputeContainer,
		health:  app.HealthCheckPath,
		public:  true,
		ingress: ingressFor(factsOf(spec.Edge)),
	}, progress)
	if err != nil {
		return nil, err
	}
	warnPreviewOpen(spec, service, progress)
	return []provider.AppContainer{{
		Name: app.App, Physical: service, URL: ran.url, Image: app.Image, Revision: ran.revision,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, _ provider.StackRef, containers []provider.AppContainer, progress edge.Progress) error {
	for _, container := range containers {
		if container.Physical == "" {
			continue
		}
		if err := p.tearDown(ctx, container.Physical, progress); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) runtimeEnv(names Names, spec provider.StackSpec) (map[string]string, error) {
	app := spec.App
	env := map[string]string{}
	if app.HealthCheckPath != "" {
		env[originguard.HealthPathVar] = app.HealthCheckPath
	}
	manifest, err := vars.Render(vars.Manifest{
		Project:     names.project,
		Region:      p.options.Region,
		Namespace:   string(names.namespace),
		Slug:        spec.Ref.Project,
		Class:       string(spec.Ref.Class),
		Environment: liveEnvironment(spec.Ref),
		Endpoint:    p.endpoint,
		Keys:        liveKeys(app.Values),
		Bindings:    liveBindings(app.Values),
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	if len(manifest) > 0 {
		env[vars.EnvVar] = string(manifest)
	}
	return env, nil
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

func mergedValues(what string, delivered, own map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(delivered)+len(own))
	maps.Copy(values, delivered)
	for _, name := range slices.Sorted(maps.Keys(own)) {
		if _, taken := values[name]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s sets %s in the environment its own spec names, and the deploy already resolved a value for %s: "+
					"a revision has one entry per name, so the spec's would silently take the place of what the deploy delivered "+
					"and the app would read a value nothing in it declared. Rename one of them",
				what, name, name)
		}
		values[name] = own[name]
	}
	return values, nil
}
