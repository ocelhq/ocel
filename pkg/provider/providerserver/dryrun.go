package providerserver

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

const (
	promotionGroupKind = "promotion"
	releaseKind        = "release"

	valuesGroupName = "values"

	reasonEdgeReconcile = "reconciled to serve this release"
	reasonPromote       = "the release this pointer would serve"

	releaseMintedAtDeploy = "<release minted at deploy>"
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

func withReleaseMintedAtDeploy(plan provider.Plan, release naming.ReleaseToken) provider.Plan {
	minted := func(name string) string { return strings.ReplaceAll(name, release.String(), releaseMintedAtDeploy) }
	groups := make([]provider.ChangeGroup, len(plan.Groups))
	for i, group := range plan.Groups {
		group.Name, group.Reason = minted(group.Name), minted(group.Reason)
		group.Changes = slices.Clone(group.Changes)
		for j, change := range group.Changes {
			group.Changes[j].Name, group.Changes[j].Reason = minted(change.Name), minted(change.Reason)
		}
		groups[i] = group
	}
	return provider.Plan{Groups: groups}
}

func (r *deployRun) planValuesGroup(ctx context.Context) (provider.ChangeGroup, error) {
	declared, err := manifestResources(r.manifest)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	published, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	changes := make([]provider.Change, 0, len(declared))
	for _, resource := range declared {
		if resource.Binding != "" {
			continue
		}
		changes = append(changes, provider.Change{
			Kind:   string(resource.Type),
			Name:   resource.Name,
			Action: provider.KeepOrCreate(slices.ContainsFunc(published, resources.BindingFor(resource))),
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
			Kind:   releaseKind,
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
