package providerkit

import (
	"context"
	"slices"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	promotionGroupKind = "promotion"
	deploymentKind     = "deployment"

	valuesGroupName   = "values"
	membraneGroupName = "membrane"

	reasonEdgeReconcile = "reconciled to serve this release"
	reasonPromote       = "the release this pointer would serve"
	reasonMembrane      = "the runtime this release's functions boot through"
)

type draft struct {
	membrane   ChangeGroup
	infra      Plan
	parameters ChangeGroup
	apps       []Plan
	edge       ChangeGroup
	promotion  ChangeGroup
}

func (d *draft) plan() Plan {
	var held Plan
	if d.membrane.Name != "" {
		held.Groups = append(held.Groups, d.membrane)
	}
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

func (r *deployRun) drawValues(ctx context.Context) (ChangeGroup, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return ChangeGroup{}, err
	}
	published, err := r.reader().Published(ctx)
	if err != nil {
		return ChangeGroup{}, err
	}
	changes := make([]Change, 0, len(resources))
	for _, resource := range resources {
		if resource.Linked {
			continue
		}
		changes = append(changes, Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: standsOrCreates(slices.ContainsFunc(published, linking(resource))),
		})
	}
	group := ChangeGroup{Kind: ParameterGroupKind, Name: valuesGroupName, Changes: changes}
	if len(changes) > 0 {
		group.Action, group.Reason = RollUp(changes)
	}
	return group, nil
}

func (r *deployRun) drawEdge() ChangeGroup {
	action := ActionUpdate
	if r.state.Edge.Empty() {
		action = ActionCreate
	}
	return ChangeGroup{
		Kind:   EdgeGroupKind,
		Name:   edge.EdgeGroupName(r.front.Kind()),
		Action: action,
		Reason: reasonEdgeReconcile,
	}
}

func (r *deployRun) drawPromotion() ChangeGroup {
	changes := make([]Change, 0, len(r.plan.Apps))
	for _, entry := range r.plan.Apps {
		changes = append(changes, Change{
			Kind:   deploymentKind,
			Name:   entry.App,
			Action: ActionCreate,
		})
	}
	group := ChangeGroup{Kind: promotionGroupKind, Name: r.plan.Pointer, Changes: changes}
	if len(changes) == 0 {
		group.Action, group.Reason = ActionUpdate, reasonPromote
		return group
	}
	group.Action, group.Reason = RollUp(changes)
	return group
}

func (r *deployRun) drawn() *planv1.ChangePlan {
	return ChangePlanProto(r.draft.plan(), r.plan.Slug, string(r.front.Kind()))
}
