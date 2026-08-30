package providerkit

import (
	"context"
	"slices"
)

const (
	functionKind  = "function"
	containerKind = "app container"

	reasonUndeclared = "this release no longer declares it"
)

func SynthesizedPlan(ctx context.Context, store ArtifactStore, plan StackPlan, standing StackResult) (Plan, error) {
	uploads, err := UploadRows(ctx, store, plan.Uploads)
	if err != nil {
		return Plan{}, err
	}
	changes := make([]Change, 0, len(plan.Resources)+len(standing.Links)+len(uploads))
	changes = append(changes, uploads...)
	for _, resource := range plan.Resources {
		changes = append(changes, Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: standsOrCreates(slices.ContainsFunc(standing.Links, linking(resource))),
		})
	}
	declared := DeclaredFunctions(plan)
	for _, function := range declared {
		changes = append(changes, Change{
			Kind:   functionKind,
			Name:   function,
			Action: standsOrCreates(slices.ContainsFunc(standing.Functions, calling(function))),
		})
	}
	containers := DeclaredContainers(plan)
	for _, container := range containers {
		changes = append(changes, Change{
			Kind:   containerKind,
			Name:   container,
			Action: standsOrCreates(slices.ContainsFunc(standing.Containers, holding(container))),
		})
	}
	for _, link := range standing.Links {
		if slices.ContainsFunc(plan.Resources, func(resource Resource) bool { return linking(resource)(link) }) {
			continue
		}
		changes = append(changes, Change{Kind: string(link.Type), Name: link.Name, Action: ActionDelete, Reason: reasonUndeclared})
	}
	for _, function := range standing.Functions {
		if slices.Contains(declared, function.Name) {
			continue
		}
		changes = append(changes, Change{Kind: functionKind, Name: function.Name, Action: ActionDelete, Reason: reasonUndeclared})
	}
	for _, container := range standing.Containers {
		if slices.Contains(containers, container.Name) {
			continue
		}
		changes = append(changes, Change{Kind: containerKind, Name: container.Name, Action: ActionDelete, Reason: reasonUndeclared})
	}
	return stackPlan(plan.Ref, changes), nil
}

func SynthesizedRemoval(ref StackRef, standing StackResult) Plan {
	changes := make([]Change, 0, len(standing.Links)+len(standing.Functions)+len(standing.Containers))
	for _, link := range standing.Links {
		changes = append(changes, Change{Kind: string(link.Type), Name: link.Name, Action: ActionDelete})
	}
	for _, function := range standing.Functions {
		changes = append(changes, Change{Kind: functionKind, Name: function.Name, Action: ActionDelete})
	}
	for _, container := range standing.Containers {
		changes = append(changes, Change{Kind: containerKind, Name: container.Name, Action: ActionDelete})
	}
	return stackPlan(ref, changes)
}

func DeclaredFunctions(plan StackPlan) []string {
	if plan.App == nil {
		return nil
	}
	names := make([]string, 0, len(plan.App.Functions))
	for _, function := range plan.App.Functions {
		names = append(names, function.Name)
	}
	return names
}

func DeclaredContainers(plan StackPlan) []string {
	if plan.App == nil || plan.App.Compute != ComputeContainer {
		return nil
	}
	return []string{plan.App.App}
}

func standsOrCreates(stands bool) ChangeAction {
	if stands {
		return ActionKeep
	}
	return ActionCreate
}

func linking(resource Resource) func(Link) bool {
	return func(link Link) bool { return link.Name == resource.Name && link.Type == resource.Type }
}

func calling(function string) func(Function) bool {
	return func(held Function) bool { return held.Name == function }
}

func holding(container string) func(AppContainer) bool {
	return func(held AppContainer) bool { return held.Name == container }
}

func stackPlan(ref StackRef, changes []Change) Plan {
	if len(changes) == 0 {
		return Plan{}
	}
	group := ChangeGroup{Kind: StackGroupKind, Name: ref.Name.String(), Changes: changes}
	group.Action, group.Reason = RollUp(changes)
	return Plan{Groups: []ChangeGroup{group}}
}
