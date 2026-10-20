package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

func runsContainer(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.Compute == provider.ComputeContainer
}

func (r *release) provisionContainer(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	work, err := container.NewDefinition(spec, r.cfg.containerTarget())
	if err != nil {
		return provider.StackResult{}, err
	}
	transformed, err := transformStackSpec(ctx, r.cfg.Namespace, r.cfg.Transform, r.registry, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	work.TransformTags = transformed.TagsFor(transformTypeContainer, work.App)
	if err := spec.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	fronts, err := readFronts(spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	infra, err := r.containers.Ensure(ctx, spec.Ref, fronts, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	work.Infra = infra
	result, err := r.runContainer(ctx, spec, work, transformed, progress)
	if err != nil {
		return provider.StackResult{}, errors.Join(err, r.abandonContainer(ctx, spec.Ref, spec.Lease, progress))
	}
	if err := transformed.refuseUnclaimed(); err != nil {
		return provider.StackResult{}, errors.Join(err, r.abandonContainer(ctx, spec.Ref, spec.Lease, progress))
	}
	workers, err := r.provisionContainerWorkers(ctx, spec, work, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result.Containers = append(result.Containers, workers...)
	if err := spec.Images.PushRemoved(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	return result, nil
}

func (r *release) runContainer(ctx context.Context, spec provider.StackSpec, work *container.Definition, transformed *transformPatches, progress progress.Log) (provider.StackResult, error) {
	var err error
	for attempt := range container.RulePlacements {
		if err = work.PlaceRule(ctx, r.cfg.Rules); err != nil {
			return provider.StackResult{}, err
		}
		var outputs auto.OutputMap
		if outputs, err = r.automation.Run(ctx, spec.Ref, r.containerProgram(spec, work, transformed), spec.Lease, progress); err == nil {
			return work.Decode(outputs)
		}
		if !container.IsPriorityTaken(err) {
			return provider.StackResult{}, err
		}
		if progress != nil && attempt+1 < container.RulePlacements {
			progress.Say(fmt.Sprintf("Placing %s's listener rule again: another deploy claimed priority %d first (attempt %d of %d)", work.App, work.Priority(), attempt+2, container.RulePlacements))
		}
	}
	return provider.StackResult{}, fmt.Errorf("place %s's listener rule: every priority it picked was claimed by another deploy before it could take it, %d times over: %w", work.App, container.RulePlacements, err)
}

func readFronts(spec provider.StackSpec) (containers.Fronts, error) {
	if !container.IsRoutedPublicly(spec) {
		return containers.Fronts{Private: true}, nil
	}
	var ranges []string
	if spec.Edge != nil {
		ranges = spec.Edge.Facts().OriginFacingRanges
	}
	if len(ranges) == 0 {
		return containers.Fronts{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s is forwarded to a public load balancer, and the edge in front of it names no addresses it forwards from, so the load balancer would answer the whole internet", spec.App.App)
	}
	return containers.Fronts{Public: ranges}, nil
}

func (r *release) abandonContainer(ctx context.Context, ref provider.StackRef, lease provider.Lease, progress progress.Log) error {
	if err := r.automation.Destroy(ctx, ref, lease, progress); err != nil {
		return err
	}
	return r.containers.ForgetConsumer(ctx, r.cfg.KeyValues, ref)
}

func (r *release) planContainer(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	infra, present, err := r.containers.Read(ctx, spec.Ref.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	public := container.IsRoutedPublicly(spec)
	if !present || (public && infra.PublicListener == "") || (!public && infra.Listener == "") {
		shared := provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   containers.StackRef(spec.Ref.Tier).Name.String(),
			Action: provider.ActionCreate,
			Reason: "the first container deploy in the " + string(spec.Ref.Tier) + " tier provisions the load balancer and cluster every container app in it shares",
			Slow:   true,
		}
		if present {
			shared.Action = provider.ActionUpdate
		}
		if present && public {
			shared.Reason = "the first container app in the " + string(spec.Ref.Tier) + " tier an edge forwards to raises the public load balancer every such app shares"
		} else if present {
			shared.Reason = "the first container app in the " + string(spec.Ref.Tier) + " tier routed through CloudFront raises the internal load balancer and VPC origin every such app shares"
		}
		groups, err := r.planNetwork(ctx, spec.Ref.Tier)
		if err != nil {
			return provider.Plan{}, err
		}
		groups = append(groups, shared, unpreviewedStack(spec.Ref))
		if hostsContainerWorkers(spec) {
			groups = append(groups, unpreviewedStack(containerWorkersRef(spec.Ref)))
		}
		return provider.Plan{Groups: groups}, nil
	}
	work, err := container.NewDefinition(spec, r.cfg.containerTarget())
	if err != nil {
		return provider.Plan{}, err
	}
	work.Infra = infra
	transformed, err := transformStackSpec(ctx, r.cfg.Namespace, r.cfg.Transform, r.registry, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	work.TransformTags = transformed.TagsFor(transformTypeContainer, work.App)
	if err := work.PlaceRule(ctx, r.cfg.Rules); err != nil {
		return provider.Plan{}, err
	}
	previewed, err := r.automation.Preview(ctx, spec.Ref, r.containerProgram(spec, work, transformed), spec.Lease, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := transformed.refuseUnclaimed(); err != nil {
		return provider.Plan{}, err
	}
	workerGroups, err := r.previewContainerWorkers(ctx, spec, work, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	previewed.Groups = append(previewed.Groups, workerGroups...)
	return previewed, nil
}

func (r *release) containerProgram(spec provider.StackSpec, work *container.Definition, transformed *transformPatches) component.Program {
	run := func(pctx *sdk.Context) error {
		if err := transformed.install(pctx); err != nil {
			return err
		}
		return work.Declare(pctx)
	}
	return component.Program{Run: run, Tags: spec.Tags, Secrets: r.appSecrets(spec)}
}

func unpreviewedStack(ref provider.StackRef) provider.ChangeGroup {
	return provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   ref.Name.String(),
		Action: provider.ActionCreate,
		Reason: provider.DetailUnavailable,
		Slow:   true,
	}
}
