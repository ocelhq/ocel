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

const (
	FunctionsPrimitive     = "Functions"
	AppContainersPrimitive = "AppContainers"
)

type Functions interface {
	ProvisionFunctions(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.Function, error)

	RemoveFunctions(ctx context.Context, ref providerkit.StackRef, functions []providerkit.Function, report providerkit.Reporter) error
}

type AppContainers interface {
	ProvisionContainers(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) ([]providerkit.AppContainer, error)

	RemoveContainers(ctx context.Context, ref providerkit.StackRef, containers []providerkit.AppContainer, report providerkit.Reporter) error
}

type ImageRetention interface {
	ReconcileImages(ctx context.Context, ref providerkit.StackRef, app, coordinate string, report providerkit.Reporter) error
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

func Releaser(records providerkit.RecordStore, artifacts providerkit.ArtifactStore, impl any) providerkit.Releaser {
	return &fanout{records: records, artifacts: artifacts, impl: impl}
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
	records   providerkit.RecordStore
	artifacts providerkit.ArtifactStore
	impl      any
}

func (f *fanout) Plan(ctx context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.Plan, error) {
	if err := f.serves(plan); err != nil {
		return providerkit.Plan{}, err
	}
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return providerkit.Plan{}, err
	}
	return providerkit.SynthesizedPlan(ctx, f.artifacts, plan, standing(recorded))
}

func (f *fanout) PlanDestroy(ctx context.Context, ref providerkit.StackRef, _ providerkit.Reporter) (providerkit.Plan, error) {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return providerkit.Plan{}, err
	}
	return providerkit.SynthesizedRemoval(ref, standing(recorded)), nil
}

func standing(recorded providerkit.Stack) providerkit.StackResult {
	return providerkit.StackResult{Links: recorded.Links, Functions: recorded.Functions, Containers: recorded.Containers}
}

func (f *fanout) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	if err := f.serves(plan); err != nil {
		return providerkit.StackResult{}, err
	}
	standUp, err := f.standingUp(plan)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	if err := f.removeOrphans(ctx, plan, recorded, report); err != nil {
		return providerkit.StackResult{}, err
	}
	if plan.App != nil {
		defer f.reconcile(ctx, plan.Ref, plan.App.App, plan.App.Image, report)
	}
	if err := plan.Images.Ship(ctx, report); err != nil {
		return providerkit.StackResult{}, err
	}
	if err := providerkit.ShipUploads(ctx, f.artifacts, plan.Uploads, report); err != nil {
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
	if standUp == nil {
		return result, nil
	}
	stood, err := standUp(ctx, report)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	result.Functions, result.Containers = stood.Functions, stood.Containers
	return result, nil
}

type standingUp func(context.Context, providerkit.Reporter) (providerkit.StackResult, error)

func (f *fanout) standingUp(plan providerkit.StackPlan) (standingUp, error) {
	if plan.App == nil {
		return nil, nil
	}
	switch plan.App.Compute {
	case providerkit.ComputeServerless:
		functions, serves := f.impl.(Functions)
		if !serves {
			return nil, lacking(plan.App, FunctionsPrimitive)
		}
		return func(ctx context.Context, report providerkit.Reporter) (providerkit.StackResult, error) {
			standing, err := functions.ProvisionFunctions(ctx, plan, report)
			return providerkit.StackResult{Functions: standing}, err
		}, nil
	case providerkit.ComputeContainer:
		containers, serves := f.impl.(AppContainers)
		if !serves {
			return nil, lacking(plan.App, AppContainersPrimitive)
		}
		return func(ctx context.Context, report providerkit.Reporter) (providerkit.StackResult, error) {
			standing, err := containers.ProvisionContainers(ctx, plan, report)
			return providerkit.StackResult{Containers: standing}, err
		}, nil
	default:
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names the compute %q, and a stack is stood up by the primitive its compute names; the computes are %v",
			plan.App.App, plan.App.Compute, providerkit.ComputeNames(providerkit.Computes()))
	}
}

func lacking(app *providerkit.AppPlan, primitive string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"app %s runs on %s compute and this provider implements no %s, so nothing here can stand it up",
		app.App, app.Compute, primitive)
}

func (f *fanout) provision(ctx context.Context, plan providerkit.StackPlan, resource providerkit.Resource, report providerkit.Reporter) (providerkit.Link, error) {
	serving, err := f.serving(resource)
	if err != nil {
		return providerkit.Link{}, err
	}
	in := Instruction{Ref: plan.Ref, Tags: plan.Tags, Links: plan.Links, Resource: resource}
	return serving.call(f.impl, ctx, in, report)
}

func (f *fanout) serves(plan providerkit.StackPlan) error {
	for _, resource := range plan.Resources {
		if _, err := f.serving(resource); err != nil {
			return err
		}
	}
	return nil
}

func (f *fanout) serving(resource providerkit.Resource) (primitive, error) {
	for _, serving := range primitives {
		if serving.kind != resource.Type {
			continue
		}
		if !serving.servedBy(f.impl) {
			break
		}
		return serving, nil
	}
	return primitive{}, providerkit.Refuse(providerkit.CodeInvalid,
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
	if err := f.removeFunctions(ctx, ref, recorded.Functions, torn, report); err != nil {
		return err
	}
	if err := f.removeContainers(ctx, ref, recorded.Containers, torn, report); err != nil {
		return err
	}
	for _, held := range recorded.Containers {
		f.reconcile(ctx, ref, held.Name, held.Image, report)
	}
	return nil
}

func (f *fanout) reconcile(ctx context.Context, ref providerkit.StackRef, app, coordinate string, report providerkit.Reporter) {
	retention, sweeps := f.impl.(ImageRetention)
	if !sweeps || coordinate == "" {
		return
	}
	if err := retention.ReconcileImages(ctx, ref, app, coordinate, report); err != nil && report != nil {
		report.Detail(fmt.Sprintf("Left %s's unreferenced images where they stand: %v", app, err))
	}
}

const (
	undeclared = "nothing here declares"
	torn       = "this destroy would take down"
)

func (f *fanout) removeFunctions(ctx context.Context, ref providerkit.StackRef, going []providerkit.Function, because string, report providerkit.Reporter) error {
	if len(going) == 0 {
		return nil
	}
	functions, serves := f.impl.(Functions)
	if !serves {
		return unownable(ref, len(going), "function", because, FunctionsPrimitive)
	}
	return functions.RemoveFunctions(ctx, ref, going, report)
}

func (f *fanout) removeContainers(ctx context.Context, ref providerkit.StackRef, going []providerkit.AppContainer, because string, report providerkit.Reporter) error {
	if len(going) == 0 {
		return nil
	}
	containers, serves := f.impl.(AppContainers)
	if !serves {
		return unownable(ref, len(going), "container", because, AppContainersPrimitive)
	}
	return containers.RemoveContainers(ctx, ref, going, report)
}

func unownable(ref providerkit.StackRef, going int, noun, because, primitive string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s holds %d %s(s) %s, and this provider implements no %s, so they would be left standing and unowned",
		ref.Name, going, noun, because, primitive)
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
	if err := f.removeOrphanFunctions(ctx, plan, recorded, report); err != nil {
		return err
	}
	return f.removeOrphanContainers(ctx, plan, recorded, report)
}

func (f *fanout) removeOrphanFunctions(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.Stack, report providerkit.Reporter) error {
	declared := providerkit.DeclaredFunctions(plan)
	var orphans []providerkit.Function
	for _, held := range recorded.Functions {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(report, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeFunctions(ctx, plan.Ref, orphans, undeclared, report)
}

func (f *fanout) removeOrphanContainers(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.Stack, report providerkit.Reporter) error {
	declared := providerkit.DeclaredContainers(plan)
	var orphans []providerkit.AppContainer
	for _, held := range recorded.Containers {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(report, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeContainers(ctx, plan.Ref, orphans, undeclared, report)
}

func reportUndeclared(report providerkit.Reporter, name string) {
	if report == nil {
		return
	}
	report.Detail(fmt.Sprintf("Removing %s: this plan no longer declares it", name))
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
