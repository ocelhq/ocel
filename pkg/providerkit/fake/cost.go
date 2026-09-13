package fake

import (
	"context"

	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	TypeFunction  = "fake_function"
	TypeContainer = "fake_container"
	TypePostgres  = "fake_postgres"
	TypeBucket    = "fake_bucket"
)

var declaredTypes = map[providerkit.BindingType]string{
	providerkit.BindingPostgres: TypePostgres,
	providerkit.BindingBucket:   TypeBucket,
}

func (p *Provider) Shape(_ context.Context, req providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	project := "project:" + req.Plan.Slug
	environment := "environment:" + req.Plan.Env
	set := &costv1.ResourceSet{
		Scopes: []*costv1.Scope{
			{Id: project, Kind: "project", Name: req.Plan.Slug},
			{Id: environment, Parent: project, Kind: "environment", Name: req.Plan.Env},
		},
	}
	for _, resource := range req.Resources {
		if typ, declared := declaredTypes[resource.Type]; declared && resource.Binding == "" {
			set.Resources = append(set.Resources, p.shaped(environment, typ, resource.Name))
		}
	}
	for _, app := range req.Plan.Apps {
		scope := environment + "/app:" + app.App
		set.Scopes = append(set.Scopes, &costv1.Scope{Id: scope, Parent: environment, Kind: "app", Name: app.App})
		if app.Compute() == providerkit.ComputeContainer {
			set.Resources = append(set.Resources, p.shaped(scope, TypeContainer, app.App))
			continue
		}
		functions := req.Functions[app.App]
		if len(functions) == 0 {
			functions = []providerkit.FunctionSpec{{Name: app.App}}
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
