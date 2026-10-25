package resources

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
)

const (
	functionKind  = "function"
	containerKind = "app container"

	reasonUndeclared = "this release no longer declares it"
	reasonBound      = "ocel.json binds it to a published record"
)

type resourcePlan func(ctx context.Context, spec provider.StackSpec, resource provider.Resource, recorded *provider.Binding) (provider.ChangeAction, string, error)

func SynthesizedPlan(ctx context.Context, spec provider.StackSpec, deployed provider.StackResult, shipped ...provider.Change) (provider.Plan, error) {
	return synthesizedPlan(ctx, spec, deployed, false, planFromRecord, shipped)
}

func planFromRecord(_ context.Context, _ provider.StackSpec, _ provider.Resource, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return provider.KeepOrCreate(recorded != nil), "", nil
}

func synthesizedPlan(ctx context.Context, spec provider.StackSpec, deployed provider.StackResult, recordsWorkers bool, plan resourcePlan, shipped []provider.Change) (provider.Plan, error) {
	images, err := spec.Images.Rows(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	changes := make([]provider.Change, 0, len(spec.Resources)+len(deployed.Bindings)+len(shipped)+len(images))
	changes = append(changes, images...)
	changes = append(changes, shipped...)
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		action, reason, err := plan(ctx, spec, resource, findRecorded(deployed.Bindings, resource))
		if err != nil {
			return provider.Plan{}, err
		}
		changes = append(changes, provider.Change{Kind: string(resource.Type), Name: resource.Name, Action: action, Reason: reason})
	}
	declared := DeclaredFunctions(spec, recordsWorkers)
	for _, function := range declared {
		changes = append(changes, provider.Change{
			Kind:   functionKind,
			Name:   function,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Functions, functionNamed(function))),
		})
	}
	containers := DeclaredContainers(spec, recordsWorkers)
	for _, container := range containers {
		changes = append(changes, provider.Change{
			Kind:   containerKind,
			Name:   container,
			Action: planContainerChange(deployed.Containers, container, functioncode.ContainerImageRef(spec)),
		})
	}
	for _, binding := range deployed.Bindings {
		reason, going := findRemovalReason(spec.Resources, binding)
		if !going {
			continue
		}
		changes = append(changes, provider.Change{Kind: string(binding.Type), Name: binding.Name, Action: provider.ActionDelete, Reason: reason})
	}
	for _, function := range deployed.Functions {
		if slices.Contains(declared, function.Name) {
			continue
		}
		changes = append(changes, provider.Change{Kind: functionKind, Name: function.Name, Action: provider.ActionDelete, Reason: reasonUndeclared})
	}
	for _, container := range deployed.Containers {
		if slices.Contains(containers, container.Name) {
			continue
		}
		changes = append(changes, provider.Change{Kind: containerKind, Name: container.Name, Action: provider.ActionDelete, Reason: reasonUndeclared})
	}
	return stackPlan(spec.Ref, changes), nil
}

func planContainerChange(deployed []provider.AppContainer, container, image string) provider.ChangeAction {
	i := slices.IndexFunc(deployed, containerNamed(container))
	switch {
	case i < 0:
		return provider.ActionCreate
	case deployed[i].Image != image:
		return provider.ActionUpdate
	default:
		return provider.ActionKeep
	}
}

func SynthesizedRemoval(ref provider.StackRef, deployed provider.StackResult) provider.Plan {
	changes := make([]provider.Change, 0, len(deployed.Bindings)+len(deployed.Functions)+len(deployed.Containers))
	for _, binding := range deployed.Bindings {
		changes = append(changes, provider.Change{Kind: string(binding.Type), Name: binding.Name, Action: provider.ActionDelete})
	}
	for _, function := range deployed.Functions {
		changes = append(changes, provider.Change{Kind: functionKind, Name: function.Name, Action: provider.ActionDelete})
	}
	for _, container := range deployed.Containers {
		changes = append(changes, provider.Change{Kind: containerKind, Name: container.Name, Action: provider.ActionDelete})
	}
	return stackPlan(ref, changes)
}

const workerPrefix = "worker:"

func WorkerName(worker string) string { return workerPrefix + worker }

func WorkerOf(name string) (string, bool) { return strings.CutPrefix(name, workerPrefix) }

func DeclaredFunctions(spec provider.StackSpec, recordsWorkers bool) []string {
	if spec.App == nil {
		return nil
	}
	names := make([]string, 0, len(spec.App.Functions)+len(spec.App.Workers))
	if spec.App.Compute != provider.ComputeContainer {
		for _, function := range spec.App.Functions {
			names = append(names, function.Name)
		}
	}
	if recordsWorkers && spec.App.Compute == provider.ComputeServerless {
		names = append(names, declaredWorkers(spec.App)...)
	}
	return names
}

func DeclaredContainers(spec provider.StackSpec, recordsWorkers bool) []string {
	if spec.App == nil || spec.App.Compute != provider.ComputeContainer {
		return nil
	}
	if !recordsWorkers {
		return []string{spec.App.App}
	}
	return append([]string{spec.App.App}, declaredWorkers(spec.App)...)
}

func declaredWorkers(app *provider.AppSpec) []string {
	names := make([]string, 0, len(app.Workers))
	for _, worker := range app.Workers {
		names = append(names, WorkerName(worker.Name))
	}
	return names
}

func BindingFor(resource provider.Resource) func(provider.Binding) bool {
	return func(binding provider.Binding) bool {
		return binding.Name == resource.Name && binding.Type == resource.Type
	}
}

func findRemovalReason(declared []provider.Resource, binding provider.Binding) (string, bool) {
	at := slices.IndexFunc(declared, func(resource provider.Resource) bool { return BindingFor(resource)(binding) })
	switch {
	case at < 0:
		return reasonUndeclared, true
	case declared[at].Binding != "":
		return reasonBound, true
	default:
		return "", false
	}
}

func functionNamed(function string) func(provider.Function) bool {
	return func(fn provider.Function) bool { return fn.Name == function }
}

func containerNamed(container string) func(provider.AppContainer) bool {
	return func(c provider.AppContainer) bool { return c.Name == container }
}

func stackPlan(ref provider.StackRef, changes []provider.Change) provider.Plan {
	if len(changes) == 0 {
		return provider.Plan{}
	}
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: ref.Name.String(), Changes: changes}
	group.Action, group.Reason = provider.RollUp(changes)
	return provider.Plan{Groups: []provider.ChangeGroup{group}}
}
