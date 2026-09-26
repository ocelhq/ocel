package resources

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ProvisionRequest struct {
	Ref  provider.StackRef
	Tags map[string]string

	Bindings provider.Bindings

	Resource provider.Resource
}

type Hooks struct {
	ProvisionPostgres func(ctx context.Context, in ProvisionRequest, progress edge.Progress) (provider.Binding, error)
	ProvisionBucket   func(ctx context.Context, in ProvisionRequest, progress edge.Progress) (provider.Binding, error)
	RemoveResource    func(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error
	Functions         *FunctionHooks
	Containers        *ContainerHooks
	Retention         *ImageRetentionHooks
}

type FunctionHooks struct {
	Provision func(ctx context.Context, spec provider.StackSpec, progress edge.Progress) ([]provider.Function, error)
	Remove    func(ctx context.Context, ref provider.StackRef, functions []provider.Function, progress edge.Progress) error
}

type ContainerHooks struct {
	Provision func(ctx context.Context, spec provider.StackSpec, progress edge.Progress) ([]provider.AppContainer, error)
	Remove    func(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, progress edge.Progress) error
}

type ImageRetentionHooks struct {
	Reconcile func(ctx context.Context, ref provider.StackRef, app, imageRef string, progress edge.Progress) error
	Forget    func(ctx context.Context, ref provider.StackRef, app string, progress edge.Progress) error
}

func ServedBindingTypes(hooks Hooks) []provider.BindingType {
	var served []provider.BindingType
	for _, primitive := range primitives {
		if primitive.of(hooks) != nil {
			served = append(served, primitive.kind)
		}
	}
	return served
}

func NewHookStacks(store records.Store, artifacts provider.ArtifactStore, hooks Hooks) provider.Stacks {
	return &hookStacks{records: store, artifacts: artifacts, hooks: hooks}
}

type provisionFunc func(ctx context.Context, in ProvisionRequest, progress edge.Progress) (provider.Binding, error)

type primitive struct {
	kind provider.BindingType
	of   func(Hooks) provisionFunc
}

var primitives = []primitive{
	{kind: provider.BindingPostgres, of: func(h Hooks) provisionFunc { return h.ProvisionPostgres }},
	{kind: provider.BindingBucket, of: func(h Hooks) provisionFunc { return h.ProvisionBucket }},
}

type hookStacks struct {
	records   records.Store
	artifacts provider.ArtifactStore
	hooks     Hooks
}

func (f *hookStacks) Plan(ctx context.Context, spec provider.StackSpec, _ edge.Progress) (provider.Plan, error) {
	if err := f.refuseUnservedResources(spec); err != nil {
		return provider.Plan{}, err
	}
	recorded, err := f.recorded(ctx, spec.Ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return SynthesizedPlan(ctx, f.artifacts, spec, recordedResult(recorded))
}

func (f *hookStacks) PlanDestroy(ctx context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return SynthesizedRemoval(ref, recordedResult(recorded)), nil
}

func recordedResult(recorded stackrecords.Stack) provider.StackResult {
	return provider.StackResult{Bindings: recorded.Bindings, Functions: recorded.Functions, Containers: recorded.Containers}
}

func (f *hookStacks) Provision(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
	if spec.App != nil {
		defer func() { _ = f.reconcile(ctx, spec.Ref, spec.App.App, spec.App.Image, progress) }()
	}
	if err := f.refuseUnservedResources(spec); err != nil {
		return provider.StackResult{}, err
	}
	provisionCompute, err := f.computeProvisioner(spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	recorded, err := f.recorded(ctx, spec.Ref)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := f.removeOrphans(ctx, spec, recorded, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := spec.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := ShipUploads(ctx, f.artifacts, spec.Uploads, progress); err != nil {
		return provider.StackResult{}, err
	}

	var result provider.StackResult
	for _, resource := range spec.Resources {
		binding, err := f.provision(ctx, spec, resource, progress)
		if err != nil {
			return provider.StackResult{}, err
		}
		if err := provider.VerifyProperties(binding); err != nil {
			return provider.StackResult{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	if provisionCompute == nil {
		return result, nil
	}
	provisioned, err := provisionCompute(ctx, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result.Functions, result.Containers = provisioned.Functions, provisioned.Containers
	return result, nil
}

type computeProvisioner func(context.Context, edge.Progress) (provider.StackResult, error)

func (f *hookStacks) computeProvisioner(spec provider.StackSpec) (computeProvisioner, error) {
	if spec.App == nil {
		return nil, nil
	}
	switch spec.App.Compute {
	case provider.ComputeServerless:
		if f.hooks.Functions == nil {
			return nil, refuseMissingComputeHooks(spec.App, "Functions")
		}
		return func(ctx context.Context, progress edge.Progress) (provider.StackResult, error) {
			functions, err := f.hooks.Functions.Provision(ctx, spec, progress)
			return provider.StackResult{Functions: functions}, err
		}, nil
	case provider.ComputeContainer:
		if f.hooks.Containers == nil {
			return nil, refuseMissingComputeHooks(spec.App, "Containers")
		}
		return func(ctx context.Context, progress edge.Progress) (provider.StackResult, error) {
			containers, err := f.hooks.Containers.Provision(ctx, spec, progress)
			return provider.StackResult{Containers: containers}, err
		}, nil
	default:
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names the compute %q, and a stack is provisioned by the primitive its compute names; the computes are %v",
			spec.App.App, spec.App.Compute, provider.ComputeNames(provider.Computes()))
	}
}

func refuseMissingComputeHooks(app *provider.AppSpec, hook string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"app %s runs on %s compute and this provider sets no %s hooks, so nothing here can provision it",
		app.App, app.Compute, hook)
}

func (f *hookStacks) provision(ctx context.Context, spec provider.StackSpec, resource provider.Resource, progress edge.Progress) (provider.Binding, error) {
	provision, err := f.provisionerFor(resource)
	if err != nil {
		return provider.Binding{}, err
	}
	in := ProvisionRequest{Ref: spec.Ref, Tags: spec.Tags, Bindings: spec.Bindings, Resource: resource}
	return provision(ctx, in, progress)
}

func (f *hookStacks) refuseUnservedResources(spec provider.StackSpec) error {
	for _, resource := range spec.Resources {
		if _, err := f.provisionerFor(resource); err != nil {
			return err
		}
	}
	return nil
}

func (f *hookStacks) provisionerFor(resource provider.Resource) (provisionFunc, error) {
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
		resource.Name, resource.Type, served(ServedBindingTypes(f.hooks)))
}

func (f *hookStacks) Destroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
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
	for _, container := range recorded.Containers {
		if err := f.forget(ctx, ref, container.Name, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	for _, container := range recorded.Containers {
		if err := f.reconcile(ctx, ref, container.Name, container.Image, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	return stopped
}

func (f *hookStacks) forget(ctx context.Context, ref provider.StackRef, app string, progress edge.Progress) error {
	if f.hooks.Retention == nil {
		return nil
	}
	err := f.hooks.Retention.Forget(ctx, ref, app, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's release window in place: %v", app, err))
	}
	return err
}

func (f *hookStacks) reconcile(ctx context.Context, ref provider.StackRef, app, imageRef string, progress edge.Progress) error {
	if f.hooks.Retention == nil || imageRef == "" {
		return nil
	}
	err := f.hooks.Retention.Reconcile(ctx, ref, app, imageRef, progress)
	if err != nil && progress != nil {
		progress.Detail(fmt.Sprintf("Left %s's unreferenced images in place: %v", app, err))
	}
	return err
}

const (
	undeclared = "nothing here declares"
	torn       = "this destroy would take down"
)

func (f *hookStacks) removeFunctions(ctx context.Context, ref provider.StackRef, going []provider.Function, because string, progress edge.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Functions == nil {
		return refuseOrphans(ref, len(going), "function", because, "Functions")
	}
	return f.hooks.Functions.Remove(ctx, ref, going, progress)
}

func (f *hookStacks) removeContainers(ctx context.Context, ref provider.StackRef, going []provider.AppContainer, because string, progress edge.Progress) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Containers == nil {
		return refuseOrphans(ref, len(going), "container", because, "Containers")
	}
	return f.hooks.Containers.Remove(ctx, ref, going, progress)
}

func refuseOrphans(ref provider.StackRef, going int, noun, because, hook string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s has %d %s(s) %s, and this provider sets no %s hooks, so they would be left running and unowned",
		ref.Name, going, noun, because, hook)
}

func (f *hookStacks) removeOrphans(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, progress edge.Progress) error {
	for _, binding := range recorded.Bindings {
		if slices.ContainsFunc(spec.Resources, func(resource provider.Resource) bool {
			return resource.Name == binding.Name && resource.Type == binding.Type
		}) {
			continue
		}
		if progress != nil {
			progress.Detail(fmt.Sprintf("Removing %s: this release no longer declares it", binding.Name))
		}
		if err := f.remove(ctx, spec.Ref, binding, progress); err != nil {
			return err
		}
	}
	if err := f.removeOrphanFunctions(ctx, spec, recorded, progress); err != nil {
		return err
	}
	return f.removeOrphanContainers(ctx, spec, recorded, progress)
}

func (f *hookStacks) removeOrphanFunctions(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, progress edge.Progress) error {
	declared := DeclaredFunctions(spec)
	var orphans []provider.Function
	for _, function := range recorded.Functions {
		if slices.Contains(declared, function.Name) {
			continue
		}
		reportUndeclared(progress, function.Name)
		orphans = append(orphans, function)
	}
	return f.removeFunctions(ctx, spec.Ref, orphans, undeclared, progress)
}

func (f *hookStacks) removeOrphanContainers(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, progress edge.Progress) error {
	declared := DeclaredContainers(spec)
	var orphans []provider.AppContainer
	for _, container := range recorded.Containers {
		if slices.Contains(declared, container.Name) {
			continue
		}
		reportUndeclared(progress, container.Name)
		orphans = append(orphans, container)
	}
	return f.removeContainers(ctx, spec.Ref, orphans, undeclared, progress)
}

func reportUndeclared(progress edge.Progress, name string) {
	if progress == nil {
		return
	}
	progress.Detail(fmt.Sprintf("Removing %s: this release no longer declares it", name))
}

func (f *hookStacks) remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error {
	if f.hooks.RemoveResource == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"binding %s is no longer declared and this provider removes no resource, so it would be left in place and unowned",
			binding.Name)
	}
	return f.hooks.RemoveResource(ctx, ref, binding, progress)
}

func (f *hookStacks) recorded(ctx context.Context, ref provider.StackRef) (stackrecords.Stack, error) {
	recorded, _, err := stackrecords.Read(ctx, f.records, ref.Class, ref.Project, ref.Name)
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
