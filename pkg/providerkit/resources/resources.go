package resources

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Instruction struct {
	Ref  provider.StackRef
	Tags map[string]string

	Bindings provider.Bindings

	Resource provider.Resource
}

type Hooks struct {
	ProvisionPostgres func(ctx context.Context, in Instruction, progress edge.Progress) (provider.Binding, error)
	ProvisionBucket   func(ctx context.Context, in Instruction, progress edge.Progress) (provider.Binding, error)
	RemoveResource    func(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error
	Functions         *FunctionHooks
	Containers        *ContainerHooks
	Retention         *RetentionHooks
}

type FunctionHooks struct {
	Provision func(ctx context.Context, plan provider.StackPlan, progress edge.Progress) ([]provider.Function, error)
	Remove    func(ctx context.Context, ref provider.StackRef, functions []provider.Function, progress edge.Progress) error
}

type ContainerHooks struct {
	Provision func(ctx context.Context, plan provider.StackPlan, progress edge.Progress) ([]provider.AppContainer, error)
	Remove    func(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, progress edge.Progress) error
}

type RetentionHooks struct {
	Reconcile func(ctx context.Context, ref provider.StackRef, app, imageRef string, progress edge.Progress) error
	Forget    func(ctx context.Context, ref provider.StackRef, app string, progress edge.Progress) error
}

func Serves(hooks Hooks) []provider.BindingType {
	var served []provider.BindingType
	for _, primitive := range primitives {
		if primitive.of(hooks) != nil {
			served = append(served, primitive.kind)
		}
	}
	return served
}

func Stacks(records records.Store, artifacts provider.ArtifactStore, hooks Hooks) provider.Stacks {
	return &fanout{records: records, artifacts: artifacts, hooks: hooks}
}

type provisioning func(ctx context.Context, in Instruction, progress edge.Progress) (provider.Binding, error)

type primitive struct {
	kind provider.BindingType
	of   func(Hooks) provisioning
}

var primitives = []primitive{
	{kind: provider.BindingPostgres, of: func(h Hooks) provisioning { return h.ProvisionPostgres }},
	{kind: provider.BindingBucket, of: func(h Hooks) provisioning { return h.ProvisionBucket }},
}

type fanout struct {
	records   records.Store
	artifacts provider.ArtifactStore
	hooks     Hooks
}

func (f *fanout) Plan(ctx context.Context, plan provider.StackPlan, _ edge.Progress) (provider.Plan, error) {
	if err := f.serves(plan); err != nil {
		return provider.Plan{}, err
	}
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return providerkit.SynthesizedPlan(ctx, f.artifacts, plan, standing(recorded))
}

func (f *fanout) PlanDestroy(ctx context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return providerkit.SynthesizedRemoval(ref, standing(recorded)), nil
}

func standing(recorded providerkit.RecordedStack) provider.StackResult {
	return provider.StackResult{Bindings: recorded.Bindings, Functions: recorded.Functions, Containers: recorded.Containers}
}

func (f *fanout) Provision(ctx context.Context, plan provider.StackPlan, progress edge.Progress) (provider.StackResult, error) {
	if plan.App != nil {
		defer func() { _ = f.reconcile(ctx, plan.Ref, plan.App.App, plan.App.Image, progress) }()
	}
	if err := f.serves(plan); err != nil {
		return provider.StackResult{}, err
	}
	standUp, err := f.standingUp(plan)
	if err != nil {
		return provider.StackResult{}, err
	}
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := f.removeOrphans(ctx, plan, recorded, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := plan.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := providerkit.ShipUploads(ctx, f.artifacts, plan.Uploads, progress); err != nil {
		return provider.StackResult{}, err
	}

	var result provider.StackResult
	for _, resource := range plan.Resources {
		binding, err := f.provision(ctx, plan, resource, progress)
		if err != nil {
			return provider.StackResult{}, err
		}
		if err := provider.VerifyProperties(binding); err != nil {
			return provider.StackResult{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	if standUp == nil {
		return result, nil
	}
	stood, err := standUp(ctx, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result.Functions, result.Containers = stood.Functions, stood.Containers
	return result, nil
}

type standingUp func(context.Context, edge.Progress) (provider.StackResult, error)

func (f *fanout) standingUp(plan provider.StackPlan) (standingUp, error) {
	if plan.App == nil {
		return nil, nil
	}
	switch plan.App.Compute {
	case provider.ComputeServerless:
		if f.hooks.Functions == nil {
			return nil, lacking(plan.App, "Functions")
		}
		return func(ctx context.Context, progress edge.Progress) (provider.StackResult, error) {
			standing, err := f.hooks.Functions.Provision(ctx, plan, progress)
			return provider.StackResult{Functions: standing}, err
		}, nil
	case provider.ComputeContainer:
		if f.hooks.Containers == nil {
			return nil, lacking(plan.App, "Containers")
		}
		return func(ctx context.Context, progress edge.Progress) (provider.StackResult, error) {
			standing, err := f.hooks.Containers.Provision(ctx, plan, progress)
			return provider.StackResult{Containers: standing}, err
		}, nil
	default:
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names the compute %q, and a stack is stood up by the primitive its compute names; the computes are %v",
			plan.App.App, plan.App.Compute, provider.ComputeNames(provider.Computes()))
	}
}

func lacking(app *provider.AppPlan, hook string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"app %s runs on %s compute and this provider sets no %s hooks, so nothing here can stand it up",
		app.App, app.Compute, hook)
}

func (f *fanout) provision(ctx context.Context, plan provider.StackPlan, resource provider.Resource, progress edge.Progress) (provider.Binding, error) {
	provision, err := f.serving(resource)
	if err != nil {
		return provider.Binding{}, err
	}
	in := Instruction{Ref: plan.Ref, Tags: plan.Tags, Bindings: plan.Bindings, Resource: resource}
	return provision(ctx, in, progress)
}

func (f *fanout) serves(plan provider.StackPlan) error {
	for _, resource := range plan.Resources {
		if _, err := f.serving(resource); err != nil {
			return err
		}
	}
	return nil
}

func (f *fanout) serving(resource provider.Resource) (provisioning, error) {
	for _, serving := range primitives {
		if serving.kind != resource.Type {
			continue
		}
		if provision := serving.of(f.hooks); provision != nil {
			return provision, nil
		}
		break
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"resource %s is a %s, and this provider serves %s",
		resource.Name, resource.Type, served(Serves(f.hooks)))
}

func (f *fanout) Destroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return err
	}
	for _, binding := range recorded.Bindings {
		if err := f.remove(ctx, ref, binding, progress); err != nil {
			return err
		}
	}
	if err := f.removeFunctions(ctx, ref, recorded.Functions, torn, progress); err != nil {
		return err
	}
	if err := f.removeContainers(ctx, ref, recorded.Containers, torn, progress); err != nil {
		return err
	}
	var stopped error
	for _, held := range recorded.Containers {
		if err := f.forget(ctx, ref, held.Name, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	for _, held := range recorded.Containers {
		if err := f.reconcile(ctx, ref, held.Name, held.Image, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	return stopped
}

func (f *fanout) forget(ctx context.Context, ref provider.StackRef, app string, progress edge.Progress) error {
	if f.hooks.Retention == nil {
		return nil
	}
	err := f.hooks.Retention.Forget(ctx, ref, app, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's release window standing: %v", app, err))
	}
	return err
}

func (f *fanout) reconcile(ctx context.Context, ref provider.StackRef, app, imageRef string, progress edge.Progress) error {
	if f.hooks.Retention == nil || imageRef == "" {
		return nil
	}
	err := f.hooks.Retention.Reconcile(ctx, ref, app, imageRef, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's unreferenced images where they stand: %v", app, err))
	}
	return err
}

const (
	undeclared = "nothing here declares"
	torn       = "this destroy would take down"
)

func (f *fanout) removeFunctions(ctx context.Context, ref provider.StackRef, going []provider.Function, because string, progress edge.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Functions == nil {
		return unownable(ref, len(going), "function", because, "Functions")
	}
	return f.hooks.Functions.Remove(ctx, ref, going, progress)
}

func (f *fanout) removeContainers(ctx context.Context, ref provider.StackRef, going []provider.AppContainer, because string, progress edge.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Containers == nil {
		return unownable(ref, len(going), "container", because, "Containers")
	}
	return f.hooks.Containers.Remove(ctx, ref, going, progress)
}

func unownable(ref provider.StackRef, going int, noun, because, hook string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s holds %d %s(s) %s, and this provider sets no %s hooks, so they would be left standing and unowned",
		ref.Name, going, noun, because, hook)
}

func (f *fanout) removeOrphans(ctx context.Context, plan provider.StackPlan, recorded providerkit.RecordedStack, progress edge.Progress) error {
	for _, binding := range recorded.Bindings {
		if slices.ContainsFunc(plan.Resources, func(resource provider.Resource) bool {
			return resource.Name == binding.Name && resource.Type == binding.Type
		}) {
			continue
		}
		if progress != nil {
			progress.Detail(fmt.Sprintf("Removing %s: this plan no longer declares it", binding.Name))
		}
		if err := f.remove(ctx, plan.Ref, binding, progress); err != nil {
			return err
		}
	}
	if err := f.removeOrphanFunctions(ctx, plan, recorded, progress); err != nil {
		return err
	}
	return f.removeOrphanContainers(ctx, plan, recorded, progress)
}

func (f *fanout) removeOrphanFunctions(ctx context.Context, plan provider.StackPlan, recorded providerkit.RecordedStack, progress edge.Progress) error {
	declared := providerkit.DeclaredFunctions(plan)
	var orphans []provider.Function
	for _, held := range recorded.Functions {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(progress, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeFunctions(ctx, plan.Ref, orphans, undeclared, progress)
}

func (f *fanout) removeOrphanContainers(ctx context.Context, plan provider.StackPlan, recorded providerkit.RecordedStack, progress edge.Progress) error {
	declared := providerkit.DeclaredContainers(plan)
	var orphans []provider.AppContainer
	for _, held := range recorded.Containers {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(progress, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeContainers(ctx, plan.Ref, orphans, undeclared, progress)
}

func reportUndeclared(progress edge.Progress, name string) {
	if progress == nil {
		return
	}
	progress.Detail(fmt.Sprintf("Removing %s: this plan no longer declares it", name))
}

func (f *fanout) remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error {
	if f.hooks.RemoveResource == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"binding %s is no longer declared and this provider removes no resource, so it would be left standing and unowned",
			binding.Name)
	}
	return f.hooks.RemoveResource(ctx, ref, binding, progress)
}

func (f *fanout) recorded(ctx context.Context, ref provider.StackRef) (providerkit.RecordedStack, error) {
	recorded, _, err := providerkit.ReadStack(ctx, f.records, ref.Class, ref.Project, ref.Name)
	return recorded, err
}

func served(kinds []provider.BindingType) string {
	if len(kinds) == 0 {
		return "no resource primitive at all"
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return fmt.Sprint(names)
}
