package providerkit

import (
	"context"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Planner interface {
	Plan(ctx context.Context, plan StackPlan, report Reporter) (ChangeGroup, error)
}

func (r *deployRun) dry(ctx context.Context) (*progressv1.OperationEvent, error) {
	planner, plans := r.provider.Releases().(Planner)
	if !plans {
		return nil, Refuse(CodeNotReady,
			"this provider cannot plan a deploy, so there is nothing --dry can show you")
	}

	var groups []ChangeGroup
	if !r.plan.Infra.IsZero() {
		group, err := r.planInfra(ctx, planner)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	for _, entry := range r.plan.Apps {
		group, err := r.planApp(ctx, planner, entry)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	groups = append(groups, r.planEdge())

	r.sender.send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Plan{Plan: ChangePlanProto(BootstrapPlan{Groups: groups}, r.plan.Slug, string(r.front.Kind()))},
	})
	return r.result(edge.Promotion{}, edge.FlipBound{})
}

func (r *deployRun) planInfra(ctx context.Context, planner Planner) (ChangeGroup, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return ChangeGroup{}, err
	}
	var group ChangeGroup
	err = r.timed(r.stages.InfraPhase, func(report Reporter) error {
		report.Say("Planning the environment's infrastructure")
		group, err = planner.Plan(ctx, StackPlan{
			Ref:       r.ref(r.plan.Infra),
			Kind:      StackInfra,
			Edge:      r.front,
			Tags:      r.plan.infraTags(),
			Resources: resources,
			Links:     r.reader(),
		}, report)
		return err
	})
	return group, err
}

func (r *deployRun) planApp(ctx context.Context, planner Planner, entry AppEntry) (ChangeGroup, error) {
	var group ChangeGroup
	err := r.timed(r.stages.AppPhases[entry.App], func(report Reporter) error {
		report.Say("Planning " + entry.App)
		grants, err := r.grants(ctx, entry)
		if err != nil {
			return err
		}
		facts, err := r.serving(entry)
		if err != nil {
			return err
		}
		values, err := r.appValues(entry, grants)
		if err != nil {
			return err
		}
		pack, err := r.pack(ctx, entry, values, report)
		if err != nil {
			return err
		}
		group, err = planner.Plan(ctx, StackPlan{
			Ref:   r.ref(entry.Stack),
			Kind:  StackApp,
			Edge:  r.front,
			Tags:  r.plan.tags(entry),
			Links: r.reader(),
			App: &AppPlan{
				App:             entry.App,
				Framework:       entry.Manifest.GetFramework(),
				Entry:           entryFunction(r.manifest, entry.App),
				Deployment:      entry.Build.DeploymentID(),
				Functions:       r.functionSpecs(entry),
				Values:          values,
				Grants:          grants,
				Routing:         facts.Routing,
				ISR:             facts.ISR,
				Bytecode:        facts.Bytecode,
				AssetPrefix:     facts.AssetPrefix,
				Membrane:        r.membrane,
				Guard:           facts.Guard,
				Packed:          pack.Carry,
				CrossesMembrane: crossesMembrane(r.crossesMembrane, grants),
			},
		}, report)
		if err != nil {
			return err
		}
		group.Name = entry.App
		return nil
	})
	return group, err
}

func (r *deployRun) planEdge() ChangeGroup {
	group := ChangeGroup{
		Kind:   EdgeGroupKind,
		Name:   edge.EdgeGroupName(r.front.Kind()),
		Action: ActionUpdate,
		Reason: WithoutDetail("the edge reconciles and promotes once every app is provisioned"),
	}
	return group
}
