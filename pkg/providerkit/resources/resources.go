package resources

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Instruction struct {
	Ref  providerkit.StackRef
	Tags map[string]string

	Links providerkit.LinkReader

	Resource providerkit.Resource
}

type Postgres interface {
	Postgres(ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error)
}

type Bucket interface {
	Bucket(ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error)
}

type Container interface {
	Container(ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error)
}

type Custom interface {
	Custom(ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error)
}

type Remover interface {
	RemoveResource(ctx context.Context, ref providerkit.StackRef, link providerkit.Link, report providerkit.Reporter) error
}

type Functions interface {
	ProvisionFunctions(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.Function, error)

	RemoveFunctions(ctx context.Context, ref providerkit.StackRef, functions []providerkit.Function, report providerkit.Reporter) error
}

func Serves(impl any) []providerkit.LinkType {
	var served []providerkit.LinkType
	for _, primitive := range primitives {
		if primitive.servedBy(impl) {
			served = append(served, primitive.kind)
		}
	}
	return served
}

func Releaser(records providerkit.RecordStore, impl any) providerkit.Releaser {
	return &fanout{records: records, impl: impl}
}

type primitive struct {
	kind     providerkit.LinkType
	servedBy func(any) bool
	call     func(any, context.Context, Instruction, providerkit.Reporter) (providerkit.Link, error)
}

var primitives = []primitive{
	{
		kind:     providerkit.LinkPostgres,
		servedBy: func(impl any) bool { _, ok := impl.(Postgres); return ok },
		call: func(impl any, ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error) {
			return impl.(Postgres).Postgres(ctx, in, report)
		},
	},
	{
		kind:     providerkit.LinkBucket,
		servedBy: func(impl any) bool { _, ok := impl.(Bucket); return ok },
		call: func(impl any, ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error) {
			return impl.(Bucket).Bucket(ctx, in, report)
		},
	},
	{
		kind:     providerkit.LinkContainer,
		servedBy: func(impl any) bool { _, ok := impl.(Container); return ok },
		call: func(impl any, ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error) {
			return impl.(Container).Container(ctx, in, report)
		},
	},
	{
		kind:     providerkit.LinkCustom,
		servedBy: func(impl any) bool { _, ok := impl.(Custom); return ok },
		call: func(impl any, ctx context.Context, in Instruction, report providerkit.Reporter) (providerkit.Link, error) {
			return impl.(Custom).Custom(ctx, in, report)
		},
	},
}

type fanout struct {
	records providerkit.RecordStore
	impl    any
}

func (f *fanout) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	if err := f.removeOrphans(ctx, plan, recorded, report); err != nil {
		return providerkit.StackResult{}, err
	}

	var result providerkit.StackResult
	for _, resource := range plan.Resources {
		link, err := f.provision(ctx, plan, resource, report)
		if err != nil {
			return providerkit.StackResult{}, err
		}
		if err := providerkit.VerifyProperties(link); err != nil {
			return providerkit.StackResult{}, err
		}
		result.Links = append(result.Links, link)
	}
	if plan.App == nil {
		return result, nil
	}
	functions, serves := f.impl.(Functions)
	if !serves {
		return result, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider stands up no functions, so it cannot serve %s's app stack", plan.Ref.Project)
	}
	if result.Functions, err = functions.ProvisionFunctions(ctx, plan, report); err != nil {
		return providerkit.StackResult{}, err
	}
	return result, nil
}

func (f *fanout) provision(ctx context.Context, plan providerkit.StackPlan, resource providerkit.Resource, report providerkit.Reporter) (providerkit.Link, error) {
	in := Instruction{Ref: plan.Ref, Tags: plan.Tags, Links: plan.Links, Resource: resource}
	for _, primitive := range primitives {
		if primitive.kind != resource.Type {
			continue
		}
		if !primitive.servedBy(f.impl) {
			break
		}
		return primitive.call(f.impl, ctx, in, report)
	}
	return providerkit.Link{}, providerkit.Refuse(providerkit.CodeInvalid,
		"resource %s is a %s, and this provider serves %s",
		resource.Name, resource.Type, served(Serves(f.impl)))
}

func (f *fanout) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return err
	}
	for _, link := range recorded.Links {
		if err := f.remove(ctx, ref, link, report); err != nil {
			return err
		}
	}
	if len(recorded.Functions) == 0 {
		return nil
	}
	functions, serves := f.impl.(Functions)
	if !serves {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s recorded %d function(s) and this provider stands up none, so nothing here can take them down",
			ref.Name, len(recorded.Functions))
	}
	return functions.RemoveFunctions(ctx, ref, recorded.Functions, report)
}

func (f *fanout) removeOrphans(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.Stack, report providerkit.Reporter) error {
	for _, link := range recorded.Links {
		if slices.ContainsFunc(plan.Resources, func(resource providerkit.Resource) bool {
			return resource.Name == link.Name && resource.Type == link.Type
		}) {
			continue
		}
		if report != nil {
			report.Detail(fmt.Sprintf("Removing %s: this plan no longer declares it", link.Name))
		}
		if err := f.remove(ctx, plan.Ref, link, report); err != nil {
			return err
		}
	}
	return nil
}

func (f *fanout) remove(ctx context.Context, ref providerkit.StackRef, link providerkit.Link, report providerkit.Reporter) error {
	remover, removes := f.impl.(Remover)
	if !removes {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"link %s is no longer declared and this provider removes no resource, so it would be left standing and unowned",
			link.Name)
	}
	return remover.RemoveResource(ctx, ref, link, report)
}

func (f *fanout) recorded(ctx context.Context, ref providerkit.StackRef) (providerkit.Stack, error) {
	recorded, _, err := providerkit.ReadStack(ctx, f.records, ref.Class, ref.Project, ref.Name)
	return recorded, err
}

func served(kinds []providerkit.LinkType) string {
	if len(kinds) == 0 {
		return "no resource primitive at all"
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return fmt.Sprint(names)
}
