package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func (p *Provider) ShapeCost(_ context.Context, req provider.ShapeRequest) (*costv1.ResourceSet, error) {
	names, err := p.named()
	if err != nil {
		return nil, err
	}
	tree := &pricing.Tree{}
	region := p.options.Region
	project := tree.Scope("", pricing.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, pricing.ScopeShared, string(req.Deploy.Tier))
	environment := tree.Scope(project, pricing.ScopeEnvironment, req.Deploy.Env)

	for _, item := range bootstrap.Resources(names, req.Deploy.Tier, false) {
		if item.Kind == bootstrap.KindQueue {
			continue
		}
		shaped, priced := item.Shape(region)
		if !priced {
			return nil, refusal.Refuse(refusal.CodeInvalid, "bootstrap item %s has no shape", item.ID())
		}
		tree.Add(shared, string(gcloud.Vendor), shaped.Type, item.Name, region, shaped.Properties)
	}
	projectKey := bootstrap.Resource{Kind: bootstrap.KindKey, Name: ports.ProjectKey(req.Deploy.Tier, req.Deploy.Slug)}
	projectDatabase := bootstrap.Resource{Kind: bootstrap.KindDatabase, Name: names.ProjectDatabase(req.Deploy.Tier, req.Deploy.Slug)}
	for _, owned := range []bootstrap.Resource{projectKey, projectDatabase} {
		shaped, _ := owned.Shape(region)
		tree.Add(shared, string(gcloud.Vendor), shaped.Type, owned.Name, region, shaped.Properties)
	}
	tree.AddShaped(shared, string(gcloud.Vendor), region, syncShape(names.EnvSourceSync(req.Deploy.Tier), region))
	if slices.ContainsFunc(req.Deploy.Apps, func(app provider.AppEntry) bool {
		return app.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext
	}) {
		tree.AddShaped(shared, string(gcloud.Vendor), region, tagDatabaseShape(names, req.Deploy.Tier, region))
	}

	front, err := p.Edges().Open(req.Edge, nil)
	if err != nil {
		return nil, err
	}
	ingress := run.IngressFor(run.FactsOf(front))
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	shapes, err := edgeShapes(front.Kind(), string(p.namespace), site)
	if err != nil {
		return nil, err
	}
	for _, shape := range shapes {
		tree.AddShaped(shared, shape.Vendor, shape.Region, shape.Shared)
		tree.AddShaped(environment, shape.Vendor, shape.Region, shape.Environment)
	}

	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(environment, pricing.ScopeApp, app.App)
		memory := run.RevisionMemory
		if app.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext {
			memory = run.FormatMemoryLimit(nextMemoryMB)
		}
		if app.Compute() == provider.ComputeContainer {
			service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, app.App)
			if err != nil {
				return nil, err
			}
			tree.Add(scope, string(gcloud.Vendor), cost.CloudRunServiceType, service, region, serviceProperties(false, app.Instances.Min, memory, ingress))
		} else {
			specs := req.Functions[app.App]
			if len(specs) == 0 {
				specs = []provider.FunctionSpec{{Name: app.App}}
			}
			for _, spec := range specs {
				service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, spec.Name)
				if err != nil {
					return nil, err
				}
				tree.Add(scope, string(gcloud.Vendor), cost.CloudRunServiceType, service, region, serviceProperties(app.Manifest.GetFramework().GetName() != buildoutput.FrameworkNext, 0, memory, ingress))
			}
		}
		for _, shape := range shapes {
			tree.AddShaped(scope, shape.Vendor, shape.Region, shape.Apps[app.App])
		}
		for _, worker := range app.Workers {
			service := names.WorkerService(req.Deploy.Slug, req.Deploy.Env, app.App, worker.Name)
			tree.Add(scope, string(gcloud.Vendor), cost.CloudRunServiceType, service, region, serviceProperties(true, 0, run.RevisionMemory, run.IngressInternal))
		}
	}

	if err := p.components().Shape(component.Shape{Tree: tree, Vendor: string(gcloud.Vendor), Names: names, Request: req, Region: region, Shared: shared, Environment: environment}, req.Resources); err != nil {
		return nil, err
	}

	return tree.Set(provider.CostSource)
}

func serviceProperties(billsPerRequest bool, minInstances int, memory, ingress string) map[string]any {
	return map[string]any{
		"ingress": ingress,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": minInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": billsPerRequest,
					"limits":   map[string]any{"cpu": run.RevisionCPU, "memory": memory},
				},
			}},
		},
	}
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req, cloudflarecost.Rates)
}

func edgeShapes(kind edge.Kind, namespace string, site pricing.EdgeSite) ([]pricing.EdgeShape, error) {
	if kind != alb.Kind && kind != cloudflare.Kind {
		return nil, nil
	}
	balancer, err := alb.Shape(site)
	if err != nil || kind == alb.Kind {
		return []pricing.EdgeShape{balancer}, err
	}
	worker, err := cloudflare.Shape(namespace, site)
	if err != nil {
		return nil, err
	}
	return []pricing.EdgeShape{balancer, worker}, nil
}
