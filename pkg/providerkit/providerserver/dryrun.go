package providerserver

import (
	"context"
	"slices"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	promotionGroupKind = "promotion"
	deploymentKind     = "deployment"

	valuesGroupName = "values"

	reasonEdgeReconcile = "reconciled to serve this release"
	reasonPromote       = "the release this pointer would serve"
)

type dryRunPlan struct {
	infra      provider.Plan
	parameters provider.ChangeGroup
	apps       []provider.Plan
	edge       provider.ChangeGroup
	promotion  provider.ChangeGroup
}

func (d *dryRunPlan) plan() provider.Plan {
	var plan provider.Plan
	plan.Groups = append(plan.Groups, d.infra.Groups...)
	if len(d.parameters.Changes) > 0 {
		plan.Groups = append(plan.Groups, d.parameters)
	}
	for _, app := range d.apps {
		plan.Groups = append(plan.Groups, app.Groups...)
	}
	plan.Groups = append(plan.Groups, d.edge, d.promotion)
	return plan
}

func (r *deployRun) planValuesGroup(ctx context.Context) (provider.ChangeGroup, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	published, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	changes := make([]provider.Change, 0, len(resources))
	for _, resource := range resources {
		if resource.Binding != "" {
			continue
		}
		changes = append(changes, provider.Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: provider.KeepOrCreate(slices.ContainsFunc(published, bindingFor(resource))),
		})
	}
	group := provider.ChangeGroup{Kind: provider.ParameterGroupKind, Name: valuesGroupName, Changes: changes}
	if len(changes) > 0 {
		group.Action, group.Reason = provider.RollUp(changes)
	}
	return group, nil
}

func (r *deployRun) planEdgeGroup() provider.ChangeGroup {
	action := provider.ActionUpdate
	if r.state.Edge.Empty() {
		action = provider.ActionCreate
	}
	return provider.ChangeGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(r.front.Kind()),
		Action: action,
		Reason: reasonEdgeReconcile,
	}
}

func (r *deployRun) planPromotionGroup() provider.ChangeGroup {
	changes := make([]provider.Change, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		changes = append(changes, provider.Change{
			Kind:   deploymentKind,
			Name:   entry.App,
			Action: provider.ActionCreate,
		})
	}
	group := provider.ChangeGroup{Kind: promotionGroupKind, Name: r.spec.Pointer, Changes: changes}
	if len(changes) == 0 {
		group.Action, group.Reason = provider.ActionUpdate, reasonPromote
		return group
	}
	group.Action, group.Reason = provider.RollUp(changes)
	return group
}

func (r *deployRun) dryRunPlanProto() *planv1.ChangePlan {
	return ChangePlanProto(r.dryRunPlan.plan(), r.spec.Slug, string(r.front.Kind()))
}

func bindingFor(resource provider.Resource) func(provider.Binding) bool {
	return func(binding provider.Binding) bool {
		return binding.Name == resource.Name && binding.Type == resource.Type
	}
}
