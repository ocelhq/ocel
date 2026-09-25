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

	Bindings providerkit.Bindings

	Resource providerkit.Resource
}

type Hooks struct {
	ProvisionPostgres   func(ctx context.Context, in Instruction, progress providerkit.Progress) (providerkit.Binding, error)
	ProvisionBucket     func(ctx context.Context, in Instruction, progress providerkit.Progress) (providerkit.Binding, error)
	RemoveResource      func(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, progress providerkit.Progress) error
	ProvisionFunctions  func(ctx context.Context, plan providerkit.StackPlan, progress providerkit.Progress) ([]providerkit.Function, error)
	RemoveFunctions     func(ctx context.Context, ref providerkit.StackRef, functions []providerkit.Function, progress providerkit.Progress) error
	ProvisionContainers func(ctx context.Context, plan providerkit.StackPlan, progress providerkit.Progress) ([]providerkit.AppContainer, error)
	RemoveContainers    func(ctx context.Context, ref providerkit.StackRef, containers []providerkit.AppContainer, progress providerkit.Progress) error
	ReconcileImages     func(ctx context.Context, ref providerkit.StackRef, app, imageRef string, progress providerkit.Progress) error
	ForgetReleases      func(ctx context.Context, ref providerkit.StackRef, app string, progress providerkit.Progress) error
}

func Serves(hooks Hooks) []providerkit.BindingType {
	var served []providerkit.BindingType
	for _, primitive := range primitives {
		if primitive.of(hooks) != nil {
			served = append(served, primitive.kind)
		}
	}
	return served
}

func Stacks(records providerkit.RecordStore, artifacts providerkit.ArtifactStore, hooks Hooks) providerkit.Stacks {
	return &fanout{records: records, artifacts: artifacts, hooks: hooks}
}

type provisioning func(ctx context.Context, in Instruction, progress providerkit.Progress) (providerkit.Binding, error)

type primitive struct {
	kind providerkit.BindingType
	of   func(Hooks) provisioning
}

var primitives = []primitive{
	{kind: providerkit.BindingPostgres, of: func(h Hooks) provisioning { return h.ProvisionPostgres }},
	{kind: providerkit.BindingBucket, of: func(h Hooks) provisioning { return h.ProvisionBucket }},
}

type fanout struct {
	records   providerkit.RecordStore
	artifacts providerkit.ArtifactStore
	hooks     Hooks
}

func (f *fanout) Plan(ctx context.Context, plan providerkit.StackPlan, _ providerkit.Progress) (providerkit.Plan, error) {
	if err := f.serves(plan); err != nil {
		return providerkit.Plan{}, err
	}
	recorded, err := f.recorded(ctx, plan.Ref)
	if err != nil {
		return providerkit.Plan{}, err
	}
	return providerkit.SynthesizedPlan(ctx, f.artifacts, plan, standing(recorded))
}

func (f *fanout) PlanDestroy(ctx context.Context, ref providerkit.StackRef, _ providerkit.Progress) (providerkit.Plan, error) {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return providerkit.Plan{}, err
	}
	return providerkit.SynthesizedRemoval(ref, standing(recorded)), nil
}

func standing(recorded providerkit.RecordedStack) providerkit.StackResult {
	return providerkit.StackResult{Bindings: recorded.Bindings, Functions: recorded.Functions, Containers: recorded.Containers}
}

func (f *fanout) Provision(ctx context.Context, plan providerkit.StackPlan, progress providerkit.Progress) (providerkit.StackResult, error) {
	if plan.App != nil {
		defer func() { _ = f.reconcile(ctx, plan.Ref, plan.App.App, plan.App.Image, progress) }()
	}
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
	if err := f.removeOrphans(ctx, plan, recorded, progress); err != nil {
		return providerkit.StackResult{}, err
	}
	if err := plan.Images.Ship(ctx, progress); err != nil {
		return providerkit.StackResult{}, err
	}
	if err := providerkit.ShipUploads(ctx, f.artifacts, plan.Uploads, progress); err != nil {
		return providerkit.StackResult{}, err
	}

	var result providerkit.StackResult
	for _, resource := range plan.Resources {
		binding, err := f.provision(ctx, plan, resource, progress)
		if err != nil {
			return providerkit.StackResult{}, err
		}
		if err := providerkit.VerifyProperties(binding); err != nil {
			return providerkit.StackResult{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	if standUp == nil {
		return result, nil
	}
	stood, err := standUp(ctx, progress)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	result.Functions, result.Containers = stood.Functions, stood.Containers
	return result, nil
}

type standingUp func(context.Context, providerkit.Progress) (providerkit.StackResult, error)

func (f *fanout) standingUp(plan providerkit.StackPlan) (standingUp, error) {
	if plan.App == nil {
		return nil, nil
	}
	switch plan.App.Compute {
	case providerkit.ComputeServerless:
		if f.hooks.ProvisionFunctions == nil {
			return nil, lacking(plan.App, "ProvisionFunctions")
		}
		return func(ctx context.Context, progress providerkit.Progress) (providerkit.StackResult, error) {
			standing, err := f.hooks.ProvisionFunctions(ctx, plan, progress)
			return providerkit.StackResult{Functions: standing}, err
		}, nil
	case providerkit.ComputeContainer:
		if f.hooks.ProvisionContainers == nil {
			return nil, lacking(plan.App, "ProvisionContainers")
		}
		return func(ctx context.Context, progress providerkit.Progress) (providerkit.StackResult, error) {
			standing, err := f.hooks.ProvisionContainers(ctx, plan, progress)
			return providerkit.StackResult{Containers: standing}, err
		}, nil
	default:
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names the compute %q, and a stack is stood up by the primitive its compute names; the computes are %v",
			plan.App.App, plan.App.Compute, providerkit.ComputeNames(providerkit.Computes()))
	}
}

func lacking(app *providerkit.AppPlan, hook string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"app %s runs on %s compute and this provider sets no %s hook, so nothing here can stand it up",
		app.App, app.Compute, hook)
}

func (f *fanout) provision(ctx context.Context, plan providerkit.StackPlan, resource providerkit.Resource, progress providerkit.Progress) (providerkit.Binding, error) {
	provision, err := f.serving(resource)
	if err != nil {
		return providerkit.Binding{}, err
	}
	in := Instruction{Ref: plan.Ref, Tags: plan.Tags, Bindings: plan.Bindings, Resource: resource}
	return provision(ctx, in, progress)
}

func (f *fanout) serves(plan providerkit.StackPlan) error {
	for _, resource := range plan.Resources {
		if _, err := f.serving(resource); err != nil {
			return err
		}
	}
	return nil
}

func (f *fanout) serving(resource providerkit.Resource) (provisioning, error) {
	for _, serving := range primitives {
		if serving.kind != resource.Type {
			continue
		}
		if provision := serving.of(f.hooks); provision != nil {
			return provision, nil
		}
		break
	}
	return nil, providerkit.Refuse(providerkit.CodeInvalid,
		"resource %s is a %s, and this provider serves %s",
		resource.Name, resource.Type, served(Serves(f.hooks)))
}

func (f *fanout) Destroy(ctx context.Context, ref providerkit.StackRef, progress providerkit.Progress) error {
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

func (f *fanout) forget(ctx context.Context, ref providerkit.StackRef, app string, progress providerkit.Progress) error {
	if f.hooks.ForgetReleases == nil {
		return nil
	}
	err := f.hooks.ForgetReleases(ctx, ref, app, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's release window standing: %v", app, err))
	}
	return err
}

func (f *fanout) reconcile(ctx context.Context, ref providerkit.StackRef, app, imageRef string, progress providerkit.Progress) error {
	if f.hooks.ReconcileImages == nil || imageRef == "" {
		return nil
	}
	err := f.hooks.ReconcileImages(ctx, ref, app, imageRef, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's unreferenced images where they stand: %v", app, err))
	}
	return err
}

const (
	undeclared = "nothing here declares"
	torn       = "this destroy would take down"
)

func (f *fanout) removeFunctions(ctx context.Context, ref providerkit.StackRef, going []providerkit.Function, because string, progress providerkit.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.RemoveFunctions == nil {
		return unownable(ref, len(going), "function", because, "RemoveFunctions")
	}
	return f.hooks.RemoveFunctions(ctx, ref, going, progress)
}

func (f *fanout) removeContainers(ctx context.Context, ref providerkit.StackRef, going []providerkit.AppContainer, because string, progress providerkit.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.RemoveContainers == nil {
		return unownable(ref, len(going), "container", because, "RemoveContainers")
	}
	return f.hooks.RemoveContainers(ctx, ref, going, progress)
}

func unownable(ref providerkit.StackRef, going int, noun, because, hook string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s holds %d %s(s) %s, and this provider sets no %s hook, so they would be left standing and unowned",
		ref.Name, going, noun, because, hook)
}

func (f *fanout) removeOrphans(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.RecordedStack, progress providerkit.Progress) error {
	for _, binding := range recorded.Bindings {
		if slices.ContainsFunc(plan.Resources, func(resource providerkit.Resource) bool {
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

func (f *fanout) removeOrphanFunctions(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.RecordedStack, progress providerkit.Progress) error {
	declared := providerkit.DeclaredFunctions(plan)
	var orphans []providerkit.Function
	for _, held := range recorded.Functions {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(progress, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeFunctions(ctx, plan.Ref, orphans, undeclared, progress)
}

func (f *fanout) removeOrphanContainers(ctx context.Context, plan providerkit.StackPlan, recorded providerkit.RecordedStack, progress providerkit.Progress) error {
	declared := providerkit.DeclaredContainers(plan)
	var orphans []providerkit.AppContainer
	for _, held := range recorded.Containers {
		if slices.Contains(declared, held.Name) {
			continue
		}
		reportUndeclared(progress, held.Name)
		orphans = append(orphans, held)
	}
	return f.removeContainers(ctx, plan.Ref, orphans, undeclared, progress)
}

func reportUndeclared(progress providerkit.Progress, name string) {
	if progress == nil {
		return
	}
	progress.Detail(fmt.Sprintf("Removing %s: this plan no longer declares it", name))
}

func (f *fanout) remove(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, progress providerkit.Progress) error {
	if f.hooks.RemoveResource == nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"binding %s is no longer declared and this provider removes no resource, so it would be left standing and unowned",
			binding.Name)
	}
	return f.hooks.RemoveResource(ctx, ref, binding, progress)
}

func (f *fanout) recorded(ctx context.Context, ref providerkit.StackRef) (providerkit.RecordedStack, error) {
	recorded, _, err := providerkit.ReadStack(ctx, f.records, ref.Class, ref.Project, ref.Name)
	return recorded, err
}

func served(kinds []providerkit.BindingType) string {
	if len(kinds) == 0 {
		return "no resource primitive at all"
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return fmt.Sprint(names)
}
