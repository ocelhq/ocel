package aws

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cost"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

const tfDataTransfer = "aws_data_transfer"

func (p *Provider) ShapeCost(ctx context.Context, req provider.ShapeRequest) (*costv1.ResourceSet, error) {
	tree := &costkit.Tree{}
	project := tree.Scope("", costkit.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, costkit.ScopeShared, string(req.Deploy.Class))
	environment := tree.Scope(project, costkit.ScopeEnvironment, req.Deploy.Env)

	var options []bootstrap.ShapeOption
	if p.options.VarsKey != "" {
		options = append(options, bootstrap.WithVarsKey(p.options.VarsKey))
	}
	features := req.Features
	if !slices.Contains(features, provider.FeatureVarsKey) {
		features = append(slices.Clone(features), provider.FeatureVarsKey)
	}
	bootstrapShape, err := bootstrap.Shape(p.namespace, string(req.Deploy.Class), features, options...)
	if err != nil {
		return nil, err
	}
	tree.AddShaped(shared, string(Vendor), p.aws.Region, bootstrapShape)

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
	site := costkit.EdgeSite{Slug: req.Deploy.Slug, Class: req.Deploy.Class, Region: p.aws.Region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, costkit.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	shape, err := edges.Shape(front.Kind(), p.namespace, site)
	if err != nil {
		return nil, err
	}
	tree.AddEdge(costkit.EdgeScopes{Shared: shared, Environment: environment}, shape)
	if !shape.BillsEgress {
		for _, app := range req.Deploy.Apps {
			tree.Add(tree.Scope(environment, costkit.ScopeApp, app.App), string(Vendor), tfDataTransfer, app.App, p.aws.Region, map[string]any{})
		}
	}
	return tree.Set(provider.CostSource)
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req, edges.Rates...)
}
