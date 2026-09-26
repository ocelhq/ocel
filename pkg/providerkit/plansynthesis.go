package providerkit

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

func SynthesizedPlan(ctx context.Context, store provider.ArtifactStore, plan provider.StackPlan, deployed provider.StackResult) (provider.Plan, error) {
	images, err := plan.Images.Rows(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	uploads, err := UploadRows(ctx, store, plan.Uploads)
	if err != nil {
		return provider.Plan{}, err
	}
	changes := make([]provider.Change, 0, len(plan.Resources)+len(deployed.Bindings)+len(uploads)+len(images))
	changes = append(changes, images...)
	changes = append(changes, uploads...)
	for _, resource := range plan.Resources {
		changes = append(changes, provider.Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Bindings, provisioning(resource))),
		})
	}
	declared := DeclaredFunctions(plan)
	for _, function := range declared {
		changes = append(changes, provider.Change{
			Kind:   functionKind,
			Name:   function,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Functions, calling(function))),
		})
	}
	containers := DeclaredContainers(plan)
	for _, container := range containers {
		changes = append(changes, provider.Change{
			Kind:   containerKind,
			Name:   container,
			Action: provider.KeepOrCreate(slices.ContainsFunc(deployed.Containers, holding(container))),
		})
	}
	for _, binding := range deployed.Bindings {
		if slices.ContainsFunc(plan.Resources, func(resource provider.Resource) bool { return provisioning(resource)(binding) }) {
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
	return stackPlan(plan.Ref, changes), nil
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

func DeclaredFunctions(plan provider.StackPlan) []string {
	if plan.App == nil {
		return nil
	}
	names := make([]string, 0, len(plan.App.Functions))
	for _, function := range plan.App.Functions {
		names = append(names, function.Name)
	}
	return names
}

func DeclaredContainers(plan provider.StackPlan) []string {
	if plan.App == nil || plan.App.Compute != provider.ComputeContainer {
		return nil
	}
	return []string{plan.App.App}
}

func provisioning(resource provider.Resource) func(provider.Binding) bool {
	return func(binding provider.Binding) bool {
		return binding.Name == resource.Name && binding.Type == resource.Type
	}
}

func calling(function string) func(provider.Function) bool {
	return func(held provider.Function) bool { return held.Name == function }
}

func holding(container string) func(provider.AppContainer) bool {
	return func(held provider.AppContainer) bool { return held.Name == container }
}

func stackPlan(ref provider.StackRef, changes []provider.Change) provider.Plan {
	if len(changes) == 0 {
		return provider.Plan{}
	}
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: ref.Name.String(), Changes: changes}
	group.Action, group.Reason = provider.RollUp(changes)
	return provider.Plan{Groups: []provider.ChangeGroup{group}}
}
