package component

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type Component struct {
	Types     []provider.BindingType
	Hooks     resources.ResourceHooks
	Parts     []Part
	Preflight func(resource provider.Resource) error
	Shape     func(shape Shape, declared []provider.Resource) error
	Grants    func(ctx context.Context, app App) ([]grants.Grant, error)
	Egress    bool
	Accounts  []bootstrap.Account
}

type Part struct {
	Name  string
	Shape func(names names.Names, tier environment.Tier, region string) []pricing.Shaped
}

type Shape struct {
	Tree        *pricing.Tree
	Vendor      string
	Names       names.Names
	Request     provider.ShapeRequest
	Region      string
	Shared      string
	Environment string
}

type App struct {
	Clients *gcloud.Clients
	Spec    provider.StackSpec
	Account string
	Member  string
	Topics  map[string]*provider.TopicSpec
}

type Registry []Component

func (r Registry) For(kind provider.BindingType) (Component, bool) {
	for _, each := range r {
		if slices.Contains(each.Types, kind) {
			return each, true
		}
	}
	return Component{}, false
}

func (r Registry) Hooks() resources.Hooks {
	var hooks resources.Hooks
	for _, each := range r {
		served := &each.Hooks
		for _, kind := range each.Types {
			switch kind {
			case provider.BindingPostgres:
				hooks.Postgres = served
			case provider.BindingBucket:
				hooks.Bucket = served
			case provider.BindingKV:
				hooks.KV = served
			case provider.BindingTopic, provider.BindingTask:
				hooks.Topic = served
			case provider.BindingRealtime:
				hooks.Realtime = served
			}
		}
	}
	return hooks
}

func (r Registry) Preflight(declared []provider.Resource) ([]string, error) {
	var parts []string
	for _, resource := range declared {
		each, ok := r.For(resource.Type)
		if !ok {
			continue
		}
		if each.Preflight != nil {
			if err := each.Preflight(resource); err != nil {
				return nil, err
			}
		}
		for _, part := range each.Parts {
			if !slices.Contains(parts, part.Name) {
				parts = append(parts, part.Name)
			}
		}
	}
	return parts, nil
}

func (r Registry) Shape(shape Shape, declared []provider.Resource) error {
	var shaped []string
	for _, each := range r {
		var own []provider.Resource
		for _, resource := range declared {
			if slices.Contains(each.Types, resource.Type) {
				own = append(own, resource)
			}
		}
		if len(own) == 0 {
			continue
		}
		if each.Shape != nil {
			if err := each.Shape(shape, own); err != nil {
				return err
			}
		}
		for _, part := range each.Parts {
			if slices.Contains(shaped, part.Name) {
				continue
			}
			shaped = append(shaped, part.Name)
			shape.Tree.AddShaped(shape.Shared, shape.Vendor, shape.Region, part.Shape(shape.Names, shape.Request.Deploy.Tier, shape.Region))
		}
	}
	return nil
}

func (r Registry) Grants(ctx context.Context, app App) ([]grants.Grant, error) {
	var granted []grants.Grant
	for _, each := range r {
		if each.Grants == nil {
			continue
		}
		own, err := each.Grants(ctx, app)
		if err != nil {
			return nil, err
		}
		granted = append(granted, own...)
	}
	return granted, nil
}

func (r Registry) Egress(bindings []provider.Binding) bool {
	return slices.ContainsFunc(bindings, func(binding provider.Binding) bool {
		each, ok := r.For(binding.Type)
		return ok && each.Egress
	})
}

func (r Registry) Unserved() []provider.BindingType {
	served := resources.ServedBindingTypes(r.Hooks())
	var unserved []provider.BindingType
	for _, each := range r {
		for _, kind := range each.Types {
			if !slices.Contains(served, kind) && !slices.Contains(unserved, kind) {
				unserved = append(unserved, kind)
			}
		}
	}
	return unserved
}

func (r Registry) Accounts() []bootstrap.Account {
	var accounts []bootstrap.Account
	for _, each := range r {
		accounts = append(accounts, each.Accounts...)
	}
	return accounts
}
