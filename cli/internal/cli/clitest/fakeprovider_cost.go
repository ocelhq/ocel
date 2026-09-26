package clitest

import (
	"context"

	"github.com/ocelhq/ocel/pkg/pricing"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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
    {"id": "fake/requests", "unit": "requests", "steps": [{"start": "0", "price": "0.000001"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/container-hours", "unit": "hours", "steps": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/postgres-hours", "unit": "hours", "steps": [{"start": "0", "price": "0.02"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/storage", "unit": "GB-month", "steps": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"}
  ]
}`

var costTable = pricing.Table{
	typeFunction: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Rate: "fake/requests", UsageBased: true,
			Quantity: r.Usage("monthly_requests", pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000})})
	},
	typeContainer: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Container", Unit: "hours", Rate: "fake/container-hours", Quantity: pricing.MonthlyHours})
	},
	typePostgres: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Database", Unit: "hours", Rate: "fake/postgres-hours", Quantity: pricing.MonthlyHours})
	},
	typeBucket: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Rate: "fake/storage", UsageBased: true,
			Quantity: r.Usage("storage_gb", pricing.Band{Light: 1, Moderate: 10, Heavy: 100})})
	},
}

func (s *deployFakeProviderServer) Shape(_ context.Context, req *contractv1.ShapeRequest) (*costv1.ResourceSet, error) {
	manifest := req.GetManifest()
	env := stackrecords.ProductionEnv
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PREVIEW {
		env = req.GetEnvironment().GetIdentity()
	}
	tree := &pricing.Tree{}
	project := tree.Scope("", pricing.ScopeProject, manifest.GetSlug())
	environment := tree.Scope(project, pricing.ScopeEnvironment, env)
	for _, resource := range manifest.GetResources() {
		if typ, shaped := costTypes[resource.GetResource().GetType()]; shaped && resource.GetBinding() == "" {
			tree.Add(environment, costVendor, typ, resource.GetLogicalName(), costRegion, map[string]any{"name": resource.GetLogicalName()})
		}
	}
	functions := map[string][]string{}
	for _, fn := range manifest.GetFunctions() {
		functions[fn.GetApp()] = append(functions[fn.GetApp()], fn.GetLogicalName())
	}
	for _, app := range manifest.GetApps() {
		scope := tree.Scope(environment, pricing.ScopeApp, app.GetName())
		if app.GetCompute() == string(provider.ComputeContainer) {
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
	return tree.Set(provider.CostSource)
}

func (s *deployFakeProviderServer) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	card, err := pricing.Load([]byte(costRates))
	if err != nil {
		return nil, err
	}
	return pricing.Estimate(card, costTable, req)
}
