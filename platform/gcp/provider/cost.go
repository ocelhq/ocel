package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

func (p *Provider) InventoryDeploy(ctx context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
	names := names.New(p.Namespace, p.Options.Project)
	tree := &pricing.Tree{}
	region := p.Options.Region
	project := tree.Scope("", pricing.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, pricing.ScopeShared, string(req.Deploy.Tier))
	environment := tree.Scope(project, pricing.ScopeEnvironment, req.Deploy.Env)

	for _, item := range bootstrap.Resources(names, req.Deploy.Tier, false) {
		entry, priced := item.Priced(region)
		if !priced {
			return nil, refusal.Refuse(refusal.CodeInvalid, "bootstrap resource %s has no priced type", item.ID())
		}
		tree.Add(shared, string(gcloud.Vendor), entry.Type, item.Name, region, entry.Properties)
	}
	use := newInventoryUse(req)
	var owned []bootstrap.Resource
	if use.keepsProjectRecords() {
		database := names.SharedDatabase(req.Deploy.Tier)
		if p.Options.OwnDatabase {
			database = names.OwnDatabase(req.Deploy.Tier, req.Deploy.Slug)
		}
		owned = append(owned, bootstrap.Resource{Kind: bootstrap.KindDatabase, Name: database})
	}
	for _, owned := range owned {
		entry, _ := owned.Priced(region)
		tree.Add(shared, string(gcloud.Vendor), entry.Type, owned.Name, region, entry.Properties)
	}
	tree.AddResources(shared, string(gcloud.Vendor), region, envsource.Inventory(names.EnvSourceSync(req.Deploy.Tier), region))

	front, err := p.Edges().Open(req.Edge, nil)
	if err != nil {
		return nil, err
	}
	ingress := run.IngressFor(run.FactsOf(front))
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	inventories, err := edgeInventories(front.Kind(), string(p.Namespace), site)
	if err != nil {
		return nil, err
	}
	for _, inventory := range inventories {
		tree.AddResources(shared, inventory.Vendor, inventory.Region, inventory.Shared)
		tree.AddResources(environment, inventory.Vendor, inventory.Region, inventory.Environment)
	}

	registry := p.Components()
	candidates := registry.TransformCandidates(req.Resources)
	for _, app := range req.Deploy.Apps {
		candidates = append(candidates, release.ServiceCandidates(app.App, app.Compute(), functionNamesOf(req, app), workerNamesOf(app))...)
	}
	transforms, err := surface.Evaluate(ctx, p.transforms, surface.Request(req.Deploy.Tier, req.Deploy.Env, candidates), p.transformed())
	if err != nil {
		return nil, err
	}

	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(environment, pricing.ScopeApp, app.App)
		served := run.Serving{Compute: app.Compute(), Billing: run.DefaultBilling(app.Compute(), servesNext(app)), Ingress: ingress}
		if servesNext(app) {
			served.Memory = next.MemoryMB
		}
		price := func(service, kind, name string, served run.Serving) error {
			properties, unknown, err := release.PriceService(names.Project(), region, served, app.App, kind, name, transforms)
			if err != nil {
				return err
			}
			tree.Add(scope, string(gcloud.Vendor), cost.CloudRunServiceType, service, region, properties, unknown...)
			return nil
		}
		if app.Compute() == provider.ComputeContainer {
			service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, app.App)
			if err != nil {
				return nil, err
			}
			served.Instances = provider.Instances{Min: app.Instances.Min}
			if err := price(service, release.TransformContainer, app.App, served); err != nil {
				return nil, err
			}
		} else {
			for _, function := range functionNamesOf(req, app) {
				service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, function)
				if err != nil {
					return nil, err
				}
				if err := price(service, release.TransformFunction, function, served); err != nil {
					return nil, err
				}
			}
		}
		for _, inventory := range inventories {
			tree.AddResources(scope, inventory.Vendor, inventory.Region, inventory.Apps[app.App])
		}
		for _, worker := range app.Workers {
			service, err := names.WorkerService(req.Deploy.Slug, req.Deploy.Env, app.App, worker.Name)
			if err != nil {
				return nil, err
			}
			if err := price(service, release.TransformWorker, worker.Name, run.Serving{Compute: provider.ComputeServerless, Ingress: run.IngressInternal}); err != nil {
				return nil, err
			}
		}
	}

	if err := registry.Inventory(component.Inventory{Tree: tree, Vendor: string(gcloud.Vendor), Names: names, Request: req, Region: region, Shared: shared, Environment: environment, Transforms: transforms}, req.Resources, use.parts()); err != nil {
		return nil, err
	}

	return tree.Set(provider.CostSource)
}

func functionNamesOf(req provider.InventoryRequest, app provider.AppEntry) []string {
	specs := req.Functions[app.App]
	if len(specs) == 0 {
		return []string{app.App}
	}
	functions := make([]string, 0, len(specs))
	for _, spec := range specs {
		functions = append(functions, spec.Name)
	}
	return functions
}

func workerNamesOf(app provider.AppEntry) []string {
	workers := make([]string, 0, len(app.Workers))
	for _, worker := range app.Workers {
		workers = append(workers, worker.Name)
	}
	return workers
}

func edgeInventories(kind edge.Kind, namespace string, site pricing.EdgeSite) ([]pricing.EdgeInventory, error) {
	var inventory pricing.EdgeInventory
	var err error
	switch kind {
	case albresources.Kind:
		inventory, err = alb.Inventory(namespace, site)
	case cloudflare.Kind:
		inventory, err = cloudflare.Inventory(site)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []pricing.EdgeInventory{inventory}, nil
}
