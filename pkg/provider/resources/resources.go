package resources

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type ProvisionRequest struct {
	Ref  provider.StackRef
	Tags map[string]string

	Bindings provider.Bindings

	Resource provider.Resource
}

type Hooks struct {
	ProvisionPostgres func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)
	ProvisionBucket   func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)
	ProvisionKV       func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)
	ProvisionTopic    func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)
	ProvisionRealtime func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)
	RemoveResource    func(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error
	Functions         *FunctionHooks
	Containers        *ContainerHooks
	Retention         *ImageRetentionHooks
	RecordsWorkers    bool
}

type FunctionHooks struct {
	Provision func(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.Function, error)
	Remove    func(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) error
	Shared    *SharedHooks[provider.Function]
}

type ContainerHooks struct {
	Provision func(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error)
	Remove    func(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) error
	Shared    *SharedHooks[provider.AppContainer]
}

type SharedHooks[T any] struct {
	Name            func(ctx context.Context, spec provider.StackSpec) ([]T, error)
	RemoveRevisions func(ctx context.Context, ref provider.StackRef, going []T, images provider.ImageStore, progress progress.Log) ([]T, error)
}

type ImageRetentionHooks struct {
	Reconcile func(ctx context.Context, ref provider.StackRef, app, imageRef string, images provider.ImageStore, progress progress.Log) error
	Forget    func(ctx context.Context, ref provider.StackRef, app string, images provider.ImageStore, progress progress.Log) error
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

func NewHookStacks(store keyvalue.Store, artifacts provider.ArtifactStore, hooks Hooks) provider.Stacks {
	return &hookStacks{keyValues: store, artifacts: artifacts, hooks: hooks}
}

type provisionFunc func(ctx context.Context, in ProvisionRequest, progress progress.Log) (provider.Binding, error)

type primitive struct {
	kind provider.BindingType
	of   func(Hooks) provisionFunc
}

var primitives = []primitive{
	{kind: provider.BindingPostgres, of: func(h Hooks) provisionFunc { return h.ProvisionPostgres }},
	{kind: provider.BindingBucket, of: func(h Hooks) provisionFunc { return h.ProvisionBucket }},
	{kind: provider.BindingKV, of: func(h Hooks) provisionFunc { return h.ProvisionKV }},
	{kind: provider.BindingTopic, of: func(h Hooks) provisionFunc { return h.ProvisionTopic }},
	{kind: provider.BindingTask, of: func(h Hooks) provisionFunc { return h.ProvisionTopic }},
	{kind: provider.BindingRealtime, of: func(h Hooks) provisionFunc { return h.ProvisionRealtime }},
}

type hookStacks struct {
	keyValues keyvalue.Store
	artifacts provider.ArtifactStore
	hooks     Hooks
}

func (f *hookStacks) Plan(ctx context.Context, spec provider.StackSpec, _ progress.Log) (provider.Plan, error) {
	if err := f.refuseUnservedResources(spec); err != nil {
		return provider.Plan{}, err
	}
	recorded, err := f.recorded(ctx, spec.Ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return synthesizedPlan(ctx, f.artifacts, spec, recordedResult(recorded), f.hooks.RecordsWorkers)
}

func (f *hookStacks) PlanDestroy(ctx context.Context, ref provider.StackRef, _ progress.Log) (provider.Plan, error) {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return provider.Plan{}, err
	}
	return SynthesizedRemoval(ref, recordedResult(recorded)), nil
}

func recordedResult(recorded stackrecords.Stack) provider.StackResult {
	return provider.StackResult{Bindings: recorded.Bindings, Functions: recorded.Functions, Containers: recorded.Containers}
}

func (f *hookStacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	if spec.App != nil {
		defer func() {
			_ = ReconcileImages(ctx, f.hooks.Retention, spec.Ref, spec.App.App, spec.App.Image, spec.Images.Store, progress)
		}()
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
	if err := f.removeOrphans(ctx, spec, recorded, spec.Images.Store, progress); err != nil {
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

type computeProvisioner func(context.Context, progress.Log) (provider.StackResult, error)

func (f *hookStacks) computeProvisioner(spec provider.StackSpec) (computeProvisioner, error) {
	if spec.App == nil {
		return nil, nil
	}
	switch spec.App.Compute {
	case provider.ComputeServerless:
		if f.hooks.Functions == nil {
			return nil, refuseMissingComputeHooks(spec.App, "Functions")
		}
		return func(ctx context.Context, progress progress.Log) (provider.StackResult, error) {
			functions, err := provisionCompute(ctx, f, spec, functionCompute(f.hooks.Functions), f.hooks.Functions.Provision, progress)
			return provider.StackResult{Functions: functions}, err
		}, nil
	case provider.ComputeContainer:
		if f.hooks.Containers == nil {
			return nil, refuseMissingComputeHooks(spec.App, "Containers")
		}
		return func(ctx context.Context, progress progress.Log) (provider.StackResult, error) {
			containers, err := provisionCompute(ctx, f, spec, containerCompute(f.hooks.Containers), f.hooks.Containers.Provision, progress)
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

func (f *hookStacks) provision(ctx context.Context, spec provider.StackSpec, resource provider.Resource, progress progress.Log) (provider.Binding, error) {
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

func (f *hookStacks) Destroy(ctx context.Context, ref provider.StackRef, images provider.ImageStore, progress progress.Log) error {
	recorded, err := f.recorded(ctx, ref)
	if err != nil {
		return err
	}
	for _, binding := range recorded.Bindings {
		if err := f.remove(ctx, ref, binding, progress); err != nil {
			return err
		}
	}
	if err := f.removeFunctions(ctx, ref, recorded.Functions, torn, images, progress); err != nil {
		return err
	}
	if err := f.removeContainers(ctx, ref, recorded.Containers, torn, images, progress); err != nil {
		return err
	}
	ran := recorded.Containers
	if recorded.Image != "" && !slices.ContainsFunc(ran, func(container provider.AppContainer) bool { return container.Image == recorded.Image }) {
		ran = append(slices.Clone(ran), provider.AppContainer{Name: recorded.App, Image: recorded.Image})
	}
	var stopped error
	for _, container := range ran {
		if err := ForgetReleases(ctx, f.hooks.Retention, ref, container.Name, images, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	for _, container := range ran {
		if err := ReconcileImages(ctx, f.hooks.Retention, ref, container.Name, container.Image, images, progress); err != nil && stopped == nil {
			stopped = err
		}
	}
	return stopped
}

func ForgetReleases(ctx context.Context, h *ImageRetentionHooks, ref provider.StackRef, app string, images provider.ImageStore, progress progress.Log) error {
	if h == nil || h.Forget == nil {
		return nil
	}
	err := h.Forget(ctx, ref, app, images, progress)
	if err != nil && progress != nil {
		progress.Warn(fmt.Sprintf("Left %s's release window in place: %v", app, err))
	}
	return err
}

func ReconcileImages(ctx context.Context, h *ImageRetentionHooks, ref provider.StackRef, app, imageRef string, images provider.ImageStore, progress progress.Log) error {
	if h == nil || h.Reconcile == nil || imageRef == "" {
		return nil
	}
	err := h.Reconcile(ctx, ref, app, imageRef, images, progress)
	if err != nil && progress != nil {
		progress.Warn(fmt.Sprintf("Left %s's unreferenced images in place: %v", app, err))
	}
	return err
}

func RemovePushedImages(ctx context.Context, images provider.ImageStore, app string, imageRefs []string, recorded func(context.Context) (map[string]bool, error), progress progress.Log) {
	for _, image := range imageRefs {
		if recorded != nil {
			kept, err := recorded(ctx)
			if err != nil {
				if progress != nil {
					progress.Warn(fmt.Sprintf("Left %s in the registry it was pushed to, as the images the project's stacks record could not be read again: %v", image, err))
				}
				continue
			}
			if kept[image] {
				continue
			}
		}
		if err := images.Remove(ctx, image); err != nil {
			if progress != nil {
				progress.Warn(fmt.Sprintf("Left %s in the registry it was pushed to: %v", image, err))
			}
			continue
		}
		if progress != nil {
			progress.Say("Removed " + app + "'s unused image " + image + " from " + images.Destination())
		}
	}
}

func SayRemovedImages(progress progress.Log, app string, removed []string) {
	if progress == nil {
		return
	}
	for _, image := range removed {
		progress.Say("Removed " + app + "'s unused image " + image)
	}
}

const (
	undeclared = "nothing here declares"
	torn       = "this destroy would take down"
)

func (f *hookStacks) removeFunctions(ctx context.Context, ref provider.StackRef, going []provider.Function, because string, images provider.ImageStore, progress progress.Log) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Functions == nil {
		return refuseOrphans(ref, len(going), "function", because, "Functions")
	}
	return removeCompute(ctx, f, ref, going, functionCompute(f.hooks.Functions), images, progress)
}

func (f *hookStacks) removeContainers(ctx context.Context, ref provider.StackRef, going []provider.AppContainer, because string, images provider.ImageStore, progress progress.Log) error {
	if len(going) == 0 {
		return nil
	}
	if f.hooks.Containers == nil {
		return refuseOrphans(ref, len(going), "container", because, "Containers")
	}
	return removeCompute(ctx, f, ref, going, containerCompute(f.hooks.Containers), images, progress)
}

func removeAll[T any](
	ctx context.Context,
	ref provider.StackRef,
	going []T,
	remove func(context.Context, provider.StackRef, []T, provider.ImageStore, progress.Log) error,
	images provider.ImageStore,
	progress progress.Log,
) error {
	if len(going) == 0 || remove == nil {
		return nil
	}
	return remove(ctx, ref, going, images, progress)
}

func refuseOrphans(ref provider.StackRef, going int, noun, because, hook string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s has %d %s(s) %s, and this provider sets no %s hooks, so they would be left running and unowned",
		ref.Name, going, noun, because, hook)
}

func (f *hookStacks) removeOrphans(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, images provider.ImageStore, progress progress.Log) error {
	for _, binding := range recorded.Bindings {
		if slices.ContainsFunc(spec.Resources, func(resource provider.Resource) bool {
			return resource.Name == binding.Name && resource.Type == binding.Type
		}) {
			continue
		}
		reportUndeclared(progress, string(binding.Type), binding.Name)
		if err := f.remove(ctx, spec.Ref, binding, progress); err != nil {
			return err
		}
	}
	if err := f.removeOrphanFunctions(ctx, spec, recorded, images, progress); err != nil {
		return err
	}
	return f.removeOrphanContainers(ctx, spec, recorded, images, progress)
}

func (f *hookStacks) removeOrphanFunctions(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, images provider.ImageStore, progress progress.Log) error {
	declared := DeclaredFunctions(spec, f.hooks.RecordsWorkers)
	var orphans []provider.Function
	for _, function := range recorded.Functions {
		if slices.Contains(declared, function.Name) {
			continue
		}
		reportUndeclared(progress, "function", function.Name)
		orphans = append(orphans, function)
	}
	return f.removeFunctions(ctx, spec.Ref, orphans, undeclared, images, progress)
}

func (f *hookStacks) removeOrphanContainers(ctx context.Context, spec provider.StackSpec, recorded stackrecords.Stack, images provider.ImageStore, progress progress.Log) error {
	declared := DeclaredContainers(spec, f.hooks.RecordsWorkers)
	var orphans []provider.AppContainer
	for _, container := range recorded.Containers {
		if slices.Contains(declared, container.Name) {
			continue
		}
		reportUndeclared(progress, "container", container.Name)
		orphans = append(orphans, container)
	}
	return f.removeContainers(ctx, spec.Ref, orphans, undeclared, images, progress)
}

func reportUndeclared(progress progress.Log, kind, name string) {
	if progress == nil {
		return
	}
	progress.Say(fmt.Sprintf("Removing %s %s: this release no longer declares it", kind, name))
}

func (f *hookStacks) remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	if f.hooks.RemoveResource == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"binding %s is no longer declared and this provider removes no resource, so it would be left in place and unowned",
			binding.Name)
	}
	return f.hooks.RemoveResource(ctx, ref, binding, progress)
}

func (f *hookStacks) recorded(ctx context.Context, ref provider.StackRef) (stackrecords.Stack, error) {
	recorded, _, err := stackrecords.Read(ctx, f.keyValues, ref.Tier, ref.Project, ref.Name)
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
