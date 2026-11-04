package aws

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

const tfDataTransfer = "aws_data_transfer"

func (p *Provider) InventoryDeploy(ctx context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
	tree := &pricing.Tree{}
	project := tree.Scope("", pricing.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, pricing.ScopeShared, string(req.Deploy.Tier))
	environment := tree.Scope(project, pricing.ScopeEnvironment, req.Deploy.Env)

	var options []account.InventoryOption
	if p.Options.VariablesKey != "" {
		options = append(options, account.WithVariablesKey(p.Options.VariablesKey))
	}
	parts := req.Parts
	if !slices.Contains(parts, provider.VariablesKeyPart) {
		parts = append(slices.Clone(parts), provider.VariablesKeyPart)
	}
	bootstrapResources, err := account.Inventory(p.Namespace, req.Deploy.Tier, parts, options...)
	if err != nil {
		return nil, err
	}
	tree.AddResources(shared, string(control.Vendor), p.Config.Region, bootstrapResources)
	for _, part := range p.tierParts(p.PartRecords()) {
		if part.Use != nil && part.Use.IsUsedBy(req.Uses, req.Edge) {
			tree.AddResources(shared, string(control.Vendor), p.Config.Region, part.Inventory(req.Deploy.Tier))
		}
	}

	if err := deploy.Inventory(ctx, p.transformPass(), string(p.Namespace), p.Config.Region, req, tree, deploy.InventoryScopes{
		Environment: environment,
		Shared:      shared,
	}); err != nil {
		return nil, err
	}

	front, err := p.Edges().Open(req.Edge, nil)
	if err != nil {
		return nil, err
	}
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: p.Config.Region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	inventory, err := edges.Inventory(front.Kind(), p.Namespace, site)
	if err != nil {
		return nil, err
	}
	tree.AddEdge(pricing.EdgeScopes{Shared: shared, Environment: environment}, inventory)
	if !inventory.BillsEgress {
		for _, app := range req.Deploy.Apps {
			tree.Add(tree.Scope(environment, pricing.ScopeApp, app.App), string(control.Vendor), tfDataTransfer, app.App, p.Config.Region, map[string]any{})
		}
	}
	return tree.Set(provider.CostSource)
}
