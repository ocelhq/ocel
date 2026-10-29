package fake

import (
	"context"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	TypeFunction  = "fake_function"
	TypeContainer = "fake_container"
	TypePostgres  = "fake_postgres"
	TypeBucket    = "fake_bucket"
)

var Rules = pricing.Table{TypeFunction: free, TypeContainer: free, TypePostgres: free, TypeBucket: free}

func free(r *pricing.Subject) { r.Free() }

var declaredTypes = map[provider.BindingType]string{
	provider.BindingPostgres: TypePostgres,
	provider.BindingBucket:   TypeBucket,
}

func (p *Provider) Inventory(_ context.Context, req provider.InventoryRequest) (*costv1.ResourceSet, error) {
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
			set.Resources = append(set.Resources, p.item(environment, typ, resource.Name))
		}
	}
	for _, app := range req.Deploy.Apps {
		scope := environment + "/app:" + app.App
		set.Scopes = append(set.Scopes, &costv1.Scope{Id: scope, Parent: environment, Kind: "app", Name: app.App})
		if app.Compute() == provider.ComputeContainer {
			set.Resources = append(set.Resources, p.item(scope, TypeContainer, app.App))
			continue
		}
		functions := req.Functions[app.App]
		if len(functions) == 0 {
			functions = []provider.FunctionSpec{{Name: app.App}}
		}
		for _, fn := range functions {
			set.Resources = append(set.Resources, p.item(scope, TypeFunction, fn.Name))
		}
	}
	return set, nil
}

func (p *Provider) item(scope, typ, name string) *costv1.Resource {
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
