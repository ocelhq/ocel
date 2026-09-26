package providerkit

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

type draft struct {
	infra      provider.Plan
	parameters provider.ChangeGroup
	apps       []provider.Plan
	edge       provider.ChangeGroup
	promotion  provider.ChangeGroup
}

func (d *draft) plan() provider.Plan {
	var held provider.Plan
	held.Groups = append(held.Groups, d.infra.Groups...)
	if len(d.parameters.Changes) > 0 {
		held.Groups = append(held.Groups, d.parameters)
	}
	for _, app := range d.apps {
		held.Groups = append(held.Groups, app.Groups...)
	}
	held.Groups = append(held.Groups, d.edge, d.promotion)
	return held
}

func (r *deployRun) drawValues(ctx context.Context) (provider.ChangeGroup, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	published, err := r.reader().Published(ctx)
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

func (r *deployRun) drawEdge() provider.ChangeGroup {
	action := provider.ActionUpdate
	if r.state.Edge.Empty() {
		action = provider.ActionCreate
	}
	return provider.ChangeGroup{
		Kind:   provider.EdgeGroupKind,
		Name:   edge.EdgeGroupName(r.front.Kind()),
		Action: action,
		Reason: reasonEdgeReconcile,
	}
}

func (r *deployRun) drawPromotion() provider.ChangeGroup {
	changes := make([]provider.Change, 0, len(r.plan.Apps))
	for _, entry := range r.plan.Apps {
		changes = append(changes, provider.Change{
			Kind:   deploymentKind,
			Name:   entry.App,
			Action: provider.ActionCreate,
		})
	}
	group := provider.ChangeGroup{Kind: promotionGroupKind, Name: r.plan.Pointer, Changes: changes}
	if len(changes) == 0 {
		group.Action, group.Reason = provider.ActionUpdate, reasonPromote
		return group
	}
	group.Action, group.Reason = provider.RollUp(changes)
	return group
}

func (r *deployRun) drawn() *planv1.ChangePlan {
	return ChangePlanProto(r.draft.plan(), r.plan.Slug, string(r.front.Kind()))
}

func bindingFor(resource provider.Resource) func(provider.Binding) bool {
	return func(binding provider.Binding) bool {
		return binding.Name == resource.Name && binding.Type == resource.Type
	}
}
