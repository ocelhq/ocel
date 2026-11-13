package component

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

type Component struct {
	Types      []provider.BindingType
	Hooks      resources.ResourceHooks
	Parts      []Part
	Preflight  func(resource provider.Resource) error
	Inventory  func(inventory Inventory, declared []provider.Resource) error
	Grants     func(ctx context.Context, app App) ([]grants.Grant, error)
	Egress     bool
	Accounts   []bootstrap.Account
	Transforms map[provider.BindingType]*Transform

	ListResources func(ctx context.Context, clients *gcloud.Clients, ref provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error)
}

type Transform struct {
	Kind    string
	Allowed surface.Allowed
}

type Part struct {
	Name      string
	Inventory func(names names.Names, tier environment.Tier, region string) []pricing.Item
}

func NewPart(part bootstrap.ResourcePart) Part {
	return Part{Name: part.Name, Inventory: part.Inventory}
}

type Inventory struct {
	Tree        *pricing.Tree
	Vendor      string
	Names       names.Names
	Request     provider.InventoryRequest
	Region      string
	Shared      string
	Environment string
	Transforms  surface.Evaluated
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

func (r Registry) WithEvaluationRequired() Registry {
	required := slices.Clone(r)
	for i := range required {
		if len(required[i].Transforms) > 0 {
			required[i].Hooks = requireEvaluated(required[i].Hooks)
		}
	}
	return required
}

func requireEvaluated(hooks resources.ResourceHooks) resources.ResourceHooks {
	plan, provision := hooks.Plan, hooks.Provision
	hooks.Plan = func(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
		if err := surface.RefuseUnevaluated(ctx, in.Ref, in.Resource); err != nil {
			return "", "", err
		}
		return plan(ctx, in, recorded)
	}
	hooks.Provision = func(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
		if err := surface.RefuseUnevaluated(ctx, in.Ref, in.Resource); err != nil {
			return provider.Binding{}, err
		}
		return provision(ctx, in, log)
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

func (r Registry) Inventory(inventory Inventory, declared []provider.Resource, used []Part) error {
	var added []string
	for _, part := range used {
		if !slices.Contains(added, part.Name) {
			added = append(added, part.Name)
			inventory.Tree.AddItems(inventory.Shared, inventory.Vendor, inventory.Region, part.Inventory(inventory.Names, inventory.Request.Deploy.Tier, inventory.Region))
		}
	}
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
		if each.Inventory != nil {
			if err := each.Inventory(inventory, own); err != nil {
				return err
			}
		}
		for _, part := range each.Parts {
			if slices.Contains(added, part.Name) {
				continue
			}
			added = append(added, part.Name)
			inventory.Tree.AddItems(inventory.Shared, inventory.Vendor, inventory.Region, part.Inventory(inventory.Names, inventory.Request.Deploy.Tier, inventory.Region))
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

func (c Component) TransformOf(kind provider.BindingType) *Transform {
	return c.Transforms[kind]
}

func (r Registry) Transformed() map[string]surface.Allowed {
	allowed := map[string]surface.Allowed{}
	for _, each := range r {
		for _, typed := range each.Transforms {
			allowed[typed.Kind] = typed.Allowed
		}
	}
	return allowed
}

func (r Registry) TransformCandidates(declared []provider.Resource) []transform.Resource {
	var candidates []transform.Resource
	for _, resource := range declared {
		each, served := r.For(resource.Type)
		if !served || resource.Binding != "" {
			continue
		}
		if target := each.TransformOf(resource.Type); target != nil {
			candidates = append(candidates, transform.Resource{Type: target.Kind, Name: resource.Name})
		}
	}
	return candidates
}
