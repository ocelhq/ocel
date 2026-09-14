package gcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
)

func serviceFor(names Names, plan providerkit.StackPlan, app *providerkit.AppPlan, function string) (string, error) {
	if app.PreviewLabel != "" {
		return names.PreviewService(app.PreviewLabel, app.App, function)
	}
	return names.Service(plan.Ref.Project, plan.Ref.Name.Env, app.App, function)
}

const previewOpenWarning = "is a preview and answers anyone who knows its Cloud Run url: the %q edge shields nothing, " +
	"and Cloud Run's invoker check would shut browsers out too. Front previews with an edge that shields the origin, or keep their urls to yourselves"

func warnPreviewOpen(plan providerkit.StackPlan, service string, report providerkit.Reporter) {
	if plan.Ref.Class != providerkit.ClassPreview || factsOf(plan.Edge).ShieldsOrigin {
		return
	}
	kind := direct.Kind
	if plan.Edge != nil {
		kind = plan.Edge.Kind()
	}
	say(report, service+" "+fmt.Sprintf(previewOpenWarning, kind))
}

func (p *Provider) ProvisionFunctions(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.Function, error) {
	app := plan.App
	if app == nil {
		return nil, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	account := names.RuntimeAccountEmail(plan.Ref.Class)
	standing := make([]providerkit.Function, 0, len(app.Functions))
	for _, spec := range app.Functions {
		if err := runsX8664(spec.Runtime.Arch, "function "+spec.Name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(spec.Image) == "" {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"function %s carries no image, and a function on Cloud Run is the container a registry coordinate names", spec.Name)
		}
		service, err := serviceFor(names, plan, app, spec.Name)
		if err != nil {
			return nil, err
		}
		values, err := carried(spec.Name, app.Values.Delivered, spec.Env)
		if err != nil {
			return nil, err
		}
		ran, err := p.stand(ctx, serving{
			service: service,
			image:   spec.Image,
			env:     values,
			account: account,
			compute: providerkit.ComputeServerless,
			public:  spec.URL,
			ingress: ingressFor(factsOf(plan.Edge)),
			memory:  spec.Memory,
			timeout: spec.Timeout,
		}, report)
		if err != nil {
			return nil, err
		}
		if spec.URL {
			warnPreviewOpen(plan, service, report)
		}
		standing = append(standing, providerkit.Function{
			Name: spec.Name, Physical: service, URL: ran.url, Revision: ran.revision,
		})
	}
	return standing, nil
}

func (p *Provider) RemoveFunctions(ctx context.Context, _ providerkit.StackRef, functions []providerkit.Function, report providerkit.Reporter) error {
	for _, function := range functions {
		if function.Physical == "" {
			continue
		}
		if err := p.tearDown(ctx, function.Physical, report); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) ProvisionContainers(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.AppContainer, error) {
	app := plan.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names no image, and a Cloud Run service runs what a registry coordinate names and nothing else", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s carries no health check path, and up means a 2xx on the path the wire named rather than on one this provider chose", app.App)
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	service, err := serviceFor(names, plan, app, app.App)
	if err != nil {
		return nil, err
	}
	values, err := carried(app.App, app.Values.Delivered, nil)
	if err != nil {
		return nil, err
	}
	ran, err := p.stand(ctx, serving{
		service: service,
		image:   app.Image,
		env:     values,
		account: names.RuntimeAccountEmail(plan.Ref.Class),
		compute: providerkit.ComputeContainer,
		health:  app.HealthCheckPath,
		public:  true,
		ingress: ingressFor(factsOf(plan.Edge)),
	}, report)
	if err != nil {
		return nil, err
	}
	warnPreviewOpen(plan, service, report)
	return []providerkit.AppContainer{{
		Name: app.App, Physical: service, URL: ran.url, Image: app.Image, Revision: ran.revision,
	}}, nil
}

func (p *Provider) RemoveContainers(ctx context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, report providerkit.Reporter) error {
	for _, container := range containers {
		if container.Physical == "" {
			continue
		}
		if err := p.tearDown(ctx, container.Physical, report); err != nil {
			return err
		}
	}
	return nil
}

func carried(what string, delivered, own map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(delivered)+len(own))
	maps.Copy(values, delivered)
	for _, name := range slices.Sorted(maps.Keys(own)) {
		if _, taken := values[name]; taken {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"%s carries %s in the environment its own spec names, and the deploy already resolved a value for %s: "+
					"a revision holds one entry per name, so the spec's would silently take the place of what the deploy delivered "+
					"and the app would read a value nothing in it declared. Rename one of them",
				what, name, name)
		}
		values[name] = own[name]
	}
	return values, nil
}

var (
	_ resources.Functions     = (*Provider)(nil)
	_ resources.AppContainers = (*Provider)(nil)
)
