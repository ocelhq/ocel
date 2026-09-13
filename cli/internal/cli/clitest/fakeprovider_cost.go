package clitest

import (
	"context"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var costProvider = fake.NewProvider(fake.Options{Region: "fake-region"})

var shapedTypes = map[resourcesv1.ResourceType]providerkit.BindingType{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: providerkit.BindingPostgres,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   providerkit.BindingBucket,
}

func (s *deployFakeProviderServer) Shape(ctx context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	manifest := req.GetManifest()
	plan := providerkit.DeployPlan{Slug: manifest.GetSlug(), Env: providerkit.ProductionEnv}
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PREVIEW {
		plan.Env = req.GetEnvironment().GetIdentity()
	}
	for _, app := range manifest.GetApps() {
		plan.Apps = append(plan.Apps, providerkit.AppEntry{App: app.GetName(), Manifest: app})
	}
	var resources []providerkit.Resource
	for _, held := range manifest.GetResources() {
		resources = append(resources, providerkit.Resource{
			Name:    held.GetLogicalName(),
			Type:    shapedTypes[held.GetResource().GetType()],
			Binding: held.GetBinding(),
		})
	}
	functions := map[string][]providerkit.FunctionSpec{}
	for _, fn := range manifest.GetFunctions() {
		functions[fn.GetApp()] = append(functions[fn.GetApp()], providerkit.FunctionSpec{Name: fn.GetLogicalName()})
	}
	set, err := costProvider.Shape(ctx, providerkit.ShapeRequest{
		Plan:      plan,
		Edge:      edge.Kind(req.GetEdge().GetKind()),
		Resources: resources,
		Functions: functions,
	})
	if err != nil {
		return nil, err
	}
	set.Source = providerkit.CostSource
	return set, nil
}

func (s *deployFakeProviderServer) Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return costProvider.Price(ctx, req)
}
