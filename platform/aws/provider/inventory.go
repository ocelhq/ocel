package aws

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

const tfDataTransfer = "aws_data_transfer"

func (p *Provider) Inventory(ctx context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
	if p.aws.Region == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "no AWS region is set: %s", control.RegionHint)
	}
	tree := &pricing.Tree{}
	project := tree.Scope("", pricing.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, pricing.ScopeShared, string(req.Deploy.Tier))
	environment := tree.Scope(project, pricing.ScopeEnvironment, req.Deploy.Env)

	var options []account.InventoryOption
	if p.options.VariablesKey != "" {
		options = append(options, account.WithVariablesKey(p.options.VariablesKey))
	}
	parts := req.Parts
	if !slices.Contains(parts, provider.VariablesKeyPart) {
		parts = append(slices.Clone(parts), provider.VariablesKeyPart)
	}
	bootstrapItems, err := account.Inventory(p.namespace, req.Deploy.Tier, parts, options...)
	if err != nil {
		return nil, err
	}
	tree.AddItems(shared, string(Vendor), p.aws.Region, bootstrapItems)
	for _, part := range p.tierParts(p.partRecords()) {
		if part.Use != nil && part.Use.IsUsedBy(req.Uses, req.Edge) {
			tree.AddItems(shared, string(Vendor), p.aws.Region, part.Inventory(req.Deploy.Tier))
		}
	}

	if err := deploy.Inventory(ctx, p.transformPass(), string(p.namespace), p.aws.Region, req, tree, deploy.InventoryScopes{
		Environment: environment,
		Shared:      shared,
	}); err != nil {
		return nil, err
	}

	front, err := p.edges().Open(req.Edge, nil)
	if err != nil {
		return nil, err
	}
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: p.aws.Region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	edgeInventory, err := edges.Inventory(front.Kind(), p.namespace, site)
	if err != nil {
		return nil, err
	}
	tree.AddEdge(pricing.EdgeScopes{Shared: shared, Environment: environment}, edgeInventory)
	if !edgeInventory.BillsEgress {
		for _, app := range req.Deploy.Apps {
			tree.Add(tree.Scope(environment, pricing.ScopeApp, app.App), string(Vendor), tfDataTransfer, app.App, p.aws.Region, map[string]any{})
		}
	}
	return tree.Set(provider.InventorySource)
}
