package provider

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cost"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

const tfDataTransfer = "aws_data_transfer"

func (p *Provider) Shape(ctx context.Context, req providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	tree := &costkit.Tree{}
	project := tree.Scope("", costkit.ScopeProject, req.Plan.Slug)
	shared := tree.Scope(project, costkit.ScopeShared, string(req.Plan.Class))
	environment := tree.Scope(project, costkit.ScopeEnvironment, req.Plan.Env)

	var options []bootstrap.ShapeOption
	if p.options.VarsKey != "" {
		options = append(options, bootstrap.WithVarsKey(p.options.VarsKey))
	}
	features := req.Features
	if !slices.Contains(features, bootstrap.FeatureVarsKey) {
		features = append(slices.Clone(features), bootstrap.FeatureVarsKey)
	}
	standing, err := bootstrap.Shape(p.namespace, string(req.Plan.Class), features, options...)
	if err != nil {
		return nil, err
	}
	tree.AddShaped(shared, string(Vendor), p.aws.Region, standing)

	if err := deploy.Shape(ctx, p.transformPass(projectRoot()), p.aws.Region, req, tree, deploy.ShapeScopes{
		Environment: environment,
		Shared:      shared,
	}); err != nil {
		return nil, err
	}

	front, err := p.edges().Open(req.Edge)
	if err != nil {
		return nil, err
	}
	site := costkit.EdgeSite{Slug: req.Plan.Slug, Class: req.Plan.Class, Region: p.aws.Region}
	for _, app := range req.Plan.Apps {
		site.Apps = append(site.Apps, costkit.EdgeApp{Name: app.App, Hostnames: providerkit.ProductionHostnames(app)})
	}
	shape, err := costkit.ShapeEdge(front, site)
	if err != nil {
		return nil, err
	}
	tree.AddEdge(costkit.EdgeScopes{Shared: shared, Environment: environment}, shape)
	if !shape.BillsEgress {
		for _, app := range req.Plan.Apps {
			tree.Add(tree.Scope(environment, costkit.ScopeApp, app.App), string(Vendor), tfDataTransfer, app.App, p.aws.Region, map[string]any{})
		}
	}
	return tree.Set(providerkit.CostSource)
}

func (p *Provider) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	edges, err := providerkit.EdgePricers(p.edges())
	if err != nil {
		return nil, err
	}
	return cost.Price(req, edges...)
}
