package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cost"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const (
	scopeProject     = "project"
	scopeShared      = "shared"
	scopeEnvironment = "environment"

	tfCloudFrontDistribution = "aws_cloudfront_distribution"
	tfAPIGatewayRestAPI      = "aws_api_gateway_rest_api"

	cloudFrontPriceClass = "PriceClass_All"
)

func (p *Provider) Shape(ctx context.Context, req providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	tree := &costkit.Tree{}
	project := tree.Scope("", scopeProject, req.Plan.Slug)
	shared := tree.Scope(project, scopeShared, string(req.Plan.Class))
	environment := tree.Scope(project, scopeEnvironment, req.Plan.Env)

	var options []bootstrap.ShapeOption
	if p.options.VarsKey != "" {
		options = append(options, bootstrap.WithVarsKey(p.options.VarsKey))
	}
	standing, err := bootstrap.Shape(p.namespace, string(req.Plan.Class), req.Features, options...)
	if err != nil {
		return nil, err
	}
	for _, item := range standing {
		tree.Add(shared, string(Vendor), item.Type, item.Name, p.aws.Region, item.Properties)
	}

	if err := deploy.Shape(ctx, p.transformPass(projectRoot()), p.aws.Region, req, tree, deploy.ShapeScopes{
		Environment: environment,
		Shared:      shared,
	}); err != nil {
		return nil, err
	}

	switch req.Edge {
	case cloudfront.Kind:
		tree.Add(environment, string(Vendor), tfCloudFrontDistribution, req.Plan.Slug, p.aws.Region, map[string]any{"price_class": cloudFrontPriceClass})
		if req.Plan.Class == providerkit.ClassPreview {
			tree.Add(shared, string(Vendor), tfCloudFrontDistribution, "preview-wildcard", p.aws.Region, map[string]any{"price_class": cloudFrontPriceClass})
		}
	case apigateway.Kind:
		tree.Add(environment, string(Vendor), tfAPIGatewayRestAPI, req.Plan.Slug, p.aws.Region, map[string]any{"endpoint_configuration": map[string]any{"types": []any{"REGIONAL"}}})
	case cloudflare.Kind:
		shape, err := cloudflare.ShapeEdge(string(p.namespace), req.Plan.Class)
		if err != nil {
			return nil, err
		}
		for _, item := range shape.Shared {
			tree.Add(shared, cloudflare.Vendor, item.Type, item.Name, "", item.Properties)
		}
		for _, item := range shape.Environment {
			tree.Add(environment, cloudflare.Vendor, item.Type, item.Name, "", item.Properties)
		}
	}
	return tree.Set(providerkit.CostSource), nil
}

func (p *Provider) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req)
}
