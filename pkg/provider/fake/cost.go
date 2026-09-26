package fake

import (
	"context"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	TypeFunction  = "fake_function"
	TypeContainer = "fake_container"
	TypePostgres  = "fake_postgres"
	TypeBucket    = "fake_bucket"
)

var declaredTypes = map[provider.BindingType]string{
	provider.BindingPostgres: TypePostgres,
	provider.BindingBucket:   TypeBucket,
}

func (p *Provider) ShapeCost(_ context.Context, req provider.ShapeRequest) (*costv1.ResourceSet, error) {
	project := "project:" + req.Deploy.Slug
	environment := "environment:" + req.Deploy.Env
	set := &costv1.ResourceSet{
		Scopes: []*costv1.Scope{
			{Id: project, Kind: "project", Name: req.Deploy.Slug},
			{Id: environment, Parent: project, Kind: "environment", Name: req.Deploy.Env},
		},
	}
	for _, resource := range req.Resources {
		if typ, declared := declaredTypes[resource.Type]; declared && resource.Binding == "" {
			set.Resources = append(set.Resources, p.shaped(environment, typ, resource.Name))
		}
	}
	for _, app := range req.Deploy.Apps {
		scope := environment + "/app:" + app.App
		set.Scopes = append(set.Scopes, &costv1.Scope{Id: scope, Parent: environment, Kind: "app", Name: app.App})
		if app.Compute() == provider.ComputeContainer {
			set.Resources = append(set.Resources, p.shaped(scope, TypeContainer, app.App))
			continue
		}
		functions := req.Functions[app.App]
		if len(functions) == 0 {
			functions = []provider.FunctionSpec{{Name: app.App}}
		}
		for _, fn := range functions {
			set.Resources = append(set.Resources, p.shaped(scope, TypeFunction, fn.Name))
		}
	}
	return set, nil
}

func (p *Provider) shaped(scope, typ, name string) *costv1.Resource {
	properties, _ := structpb.NewStruct(map[string]any{"name": name})
	return &costv1.Resource{
		Id:         scope + "/" + typ + ":" + name,
		Scope:      scope,
		Vendor:     string(Vendor),
		Type:       typ,
		Name:       name,
		Region:     p.options.Region,
		Properties: properties,
	}
}

const rates = `{
  "version": "2026-01-01",
  "currency": "USD",
  "rates": [
    {"id": "fake/requests", "unit": "requests", "steps": [{"start": "0", "price": "0.000001"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/container-hours", "unit": "hours", "steps": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/postgres-hours", "unit": "hours", "steps": [{"start": "0", "price": "0.02"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"},
    {"id": "fake/storage", "unit": "GB-month", "steps": [{"start": "0", "price": "0.01"}], "source": "https://fake.example/pricing", "verified": "2026-01-01"}
  ]
}`

var requestsBand = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}

var storageBand = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}

var table = pricing.Table{
	TypeFunction: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Rate: "fake/requests", Quantity: r.Usage("monthly_requests", requestsBand), UsageBased: true})
	},
	TypeContainer: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Container", Unit: "hours", Rate: "fake/container-hours", Quantity: pricing.MonthlyHours})
	},
	TypePostgres: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Database", Unit: "hours", Rate: "fake/postgres-hours", Quantity: pricing.MonthlyHours})
	},
	TypeBucket: func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", Rate: "fake/storage", Quantity: r.Usage("storage_gb", storageBand), UsageBased: true})
	},
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	card, err := pricing.Load([]byte(rates))
	if err != nil {
		return nil, err
	}
	return pricing.Estimate(card, table, req)
}
