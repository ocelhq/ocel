package clitest

import (
	"context"

	"github.com/ocelhq/ocel/pkg/costkit"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	costVendor    = "fake"
	costRegion    = "fake-region"
	typeFunction  = "fake_function"
	typeContainer = "fake_container"
	typePostgres  = "fake_postgres"
	typeBucket    = "fake_bucket"
)

var costTypes = map[resourcesv1.ResourceType]string{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: typePostgres,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   typeBucket,
}

const costRates = `{
  "version": "2026-01-01",
  "currency": "USD",
  "rates": [
    {"id": "fake/requests", "unit": "requests", "tiers": [{"start": "0", "price": "0.000001"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/container-hours", "unit": "hours", "tiers": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/postgres-hours", "unit": "hours", "tiers": [{"start": "0", "price": "0.02"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/storage", "unit": "GB-month", "tiers": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"}
  ]
}`

var costTable = costkit.Table{
	typeFunction: func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "fake/requests", UsageBased: true,
			Quantity: r.Usage("monthly_requests", costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000})})
	},
	typeContainer: func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Container", Unit: "hours", Rate: "fake/container-hours", Quantity: costkit.MonthlyHours})
	},
	typePostgres: func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Database", Unit: "hours", Rate: "fake/postgres-hours", Quantity: costkit.MonthlyHours})
	},
	typeBucket: func(r *costkit.Subject) {
		r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "fake/storage", UsageBased: true,
			Quantity: r.Usage("storage_gb", costkit.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
}

func (s *deployFakeProviderServer) Shape(_ context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	manifest := req.GetManifest()
	env := providerkit.ProductionEnv
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PREVIEW {
		env = req.GetEnvironment().GetIdentity()
	}
	tree := &costkit.Tree{}
	project := tree.Scope("", costkit.ScopeProject, manifest.GetSlug())
	environment := tree.Scope(project, costkit.ScopeEnvironment, env)
	for _, held := range manifest.GetResources() {
		if typ, shaped := costTypes[held.GetResource().GetType()]; shaped && held.GetBinding() == "" {
			tree.Add(environment, costVendor, typ, held.GetLogicalName(), costRegion, map[string]any{"name": held.GetLogicalName()})
		}
	}
	functions := map[string][]string{}
	for _, fn := range manifest.GetFunctions() {
		functions[fn.GetApp()] = append(functions[fn.GetApp()], fn.GetLogicalName())
	}
	for _, app := range manifest.GetApps() {
		scope := tree.Scope(environment, costkit.ScopeApp, app.GetName())
		if app.GetCompute() == string(providerkit.ComputeContainer) {
			tree.Add(scope, costVendor, typeContainer, app.GetName(), costRegion, map[string]any{"name": app.GetName()})
			continue
		}
		names := functions[app.GetName()]
		if len(names) == 0 {
			names = []string{app.GetName()}
		}
		for _, name := range names {
			tree.Add(scope, costVendor, typeFunction, name, costRegion, map[string]any{"name": name})
		}
	}
	return tree.Set(providerkit.CostSource)
}

func (s *deployFakeProviderServer) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	card, err := costkit.Load([]byte(costRates))
	if err != nil {
		return nil, err
	}
	return costkit.Estimate(card, costTable, req)
}
