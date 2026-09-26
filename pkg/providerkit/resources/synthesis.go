package resources

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const (
	functionKind  = "function"
	containerKind = "app container"

	reasonUndeclared = "this release no longer declares it"
)

func SynthesizedPlan(ctx context.Context, store provider.ArtifactStore, spec provider.StackSpec, deployed provider.StackResult) (provider.Plan, error) {
	images, err := spec.Images.Rows(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	uploads, err := UploadRows(ctx, store, spec.Uploads)
	if err != nil {
		return provider.Plan{}, err
	}
	changes := make([]provider.Change, 0, len(spec.Resources)+len(deployed.Bindings)+len(uploads)+len(images))
	changes = append(changes, images...)
	changes = append(changes, uploads...)
	for _, resource := range spec.Resources {
		changes = append(changes, provider.Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Bindings, bindingFor(resource))),
		})
	}
	declared := DeclaredFunctions(spec)
	for _, function := range declared {
		changes = append(changes, provider.Change{
			Kind:   functionKind,
			Name:   function,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Functions, functionNamed(function))),
		})
	}
	containers := DeclaredContainers(spec)
	for _, container := range containers {
		changes = append(changes, provider.Change{
			Kind:   containerKind,
			Name:   container,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Containers, containerNamed(container))),
		})
	}
	for _, binding := range deployed.Bindings {
		if slices.ContainsFunc(spec.Resources, func(resource provider.Resource) bool { return bindingFor(resource)(binding) }) {
			continue
		}
		changes = append(changes, provider.Change{Kind: string(binding.Type), Name: binding.Name, Action: provider.ActionDelete, Reason: reasonUndeclared})
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

func DeclaredFunctions(spec provider.StackSpec) []string {
	if spec.App == nil {
		return nil
	}
	names := make([]string, 0, len(spec.App.Functions))
	for _, function := range spec.App.Functions {
		names = append(names, function.Name)
	}
	return names
}

func DeclaredContainers(spec provider.StackSpec) []string {
	if spec.App == nil || spec.App.Compute != provider.ComputeContainer {
		return nil
	}
	return []string{spec.App.App}
}

func bindingFor(resource provider.Resource) func(provider.Binding) bool {
	return func(binding provider.Binding) bool {
		return binding.Name == resource.Name && binding.Type == resource.Type
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
