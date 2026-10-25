package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/worker"
)

const (
	workersKey          = "workers"
	containerWorkersKey = "containers"
)

func workersAt(ref provider.StackRef) records.Key {
	path := []string{workersKey, ref.Name.Env, ref.Name.App}
	if ref.Name.HoldsContainerWorkers() {
		path = append(path, containerWorkersKey)
	}
	return stackrecords.StacksPartition(ref.Tier, ref.Project).Key(path...)
}

func (r *release) provisionWorkers(ctx context.Context, spec provider.StackSpec, work *appWork, progress progress.Log) ([]provider.Function, error) {
	if !hostsWorkersStack(spec, work) {
		return nil, nil
	}
	workers, err := worker.NewStack(spec, work.functions, work.role, work.network, r.cfg.workerTarget())
	if err != nil {
		return nil, err
	}
	ref := workersRef(spec.Ref)
	if work.network != nil {
		if err := r.functionNetwork.RecordConsumer(ctx, ref); err != nil {
			return nil, err
		}
	}
	if err := r.recordWorkers(ctx, ref); err != nil {
		return nil, fmt.Errorf("record the workers of %s: %w", spec.App.App, err)
	}
	outputs, err := r.automation.Run(ctx, ref, component.Program{Run: workers.Declare, Tags: spec.Tags}, spec.Lease, progress)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	result, err := workers.Decode(outputs)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	if work.network == nil {
		if err := r.functionNetwork.Release(ctx, r.cfg.KeyValues, ref, progress); err != nil {
			return nil, err
		}
	}
	return result.Functions, nil
}

func hostsWorkersStack(spec provider.StackSpec, work *appWork) bool {
	return spec.App != nil && work != nil && spec.Ref.Name.Env != "" && len(spec.App.Workers) > 0
}

func needsWorkersStack(app provider.LiveApp, stack naming.StackName) bool {
	return len(app.Workers) > 0 && (app.Compute == provider.ComputeContainer) == stack.HoldsContainerWorkers()
}

func workersRef(app provider.StackRef) provider.StackRef {
	return provider.StackRef{Project: app.Project, Tier: app.Tier, Name: worker.StackName(app.Name.Env, app.Name.App)}
}

func containerWorkersRef(app provider.StackRef) provider.StackRef {
	return provider.StackRef{Project: app.Project, Tier: app.Tier, Name: naming.ContainerWorkersStack(app.Name.Env, app.Name.App)}
}

func (r *release) recordWorkers(ctx context.Context, ref provider.StackRef) error {
	if r.cfg.KeyValues == nil {
		return nil
	}
	entry, err := records.ReadOrEmpty(ctx, r.cfg.KeyValues, workersAt(ref))
	if err != nil || len(entry.Value) > 0 {
		return err
	}
	entry.Value = []byte("{}")
	_, err = r.cfg.KeyValues.Write(ctx, entry)
	return err
}

func (r *release) hasRecordedWorkers(ctx context.Context, ref provider.StackRef) (bool, error) {
	if r.cfg.KeyValues == nil {
		return false, nil
	}
	_, err := r.cfg.KeyValues.Read(ctx, workersAt(ref))
	if errors.Is(err, records.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *release) destroyWorkersStack(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	ref := req.Ref
	if err := r.automation.Destroy(ctx, ref, req.Lease, progress); err != nil {
		return fmt.Errorf("take down the workers of %s: %w", ref.Name.App, err)
	}
	if ref.Name.HoldsContainerWorkers() {
		if err := releasePart(ctx, r.containers, r.cfg.KeyValues, req, progress); err != nil {
			return err
		}
	}
	if err := records.Forget(ctx, r.cfg.KeyValues, workersAt(ref)); err != nil {
		return err
	}
	return releasePart(ctx, r.functionNetwork, r.cfg.KeyValues, req, progress)
}

func (r *release) previewWorkers(ctx context.Context, spec provider.StackSpec, work *appWork, progress progress.Log) ([]provider.ChangeGroup, error) {
	if !hostsWorkersStack(spec, work) {
		return nil, nil
	}
	workers, err := worker.NewStack(spec, work.functions, work.role, work.network, r.cfg.workerTarget())
	if err != nil {
		return nil, err
	}
	ref := workersRef(spec.Ref)
	planned, err := r.automation.Preview(ctx, ref, component.Program{Run: workers.Declare, Tags: spec.Tags}, spec.Lease, progress)
	return planned.Groups, err
}

func (r *release) listRecordedWorkers(ctx context.Context, app provider.StackRef) ([]provider.StackRef, error) {
	if app.Name.IsInfra() || app.Name.Release.IsZero() || app.Name.Env == "" {
		return nil, nil
	}
	var recorded []provider.StackRef
	for _, ref := range []provider.StackRef{workersRef(app), containerWorkersRef(app)} {
		found, err := r.hasRecordedWorkers(ctx, ref)
		if err != nil {
			return nil, err
		}
		if found {
			recorded = append(recorded, ref)
		}
	}
	return recorded, nil
}

func (r *release) listReleasesOfApp(ctx context.Context, app provider.StackRef) ([]stackrecords.NamedStack, error) {
	recorded, err := stackrecords.List(ctx, r.cfg.KeyValues, app.Tier, app.Project)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(recorded, func(other stackrecords.NamedStack) bool {
		return other.Name.Env != app.Name.Env || other.Name.App != app.Name.App || other.Name.Release.IsZero()
	}), nil
}

func (r *release) listWorkersOfLastRelease(ctx context.Context, app provider.StackRef) ([]provider.StackRef, error) {
	recorded, err := r.listRecordedWorkers(ctx, app)
	if err != nil || len(recorded) == 0 {
		return nil, err
	}
	releases, err := r.listReleasesOfApp(ctx, app)
	if err != nil {
		return nil, err
	}
	for _, other := range releases {
		if other.Name != app.Name {
			return nil, nil
		}
	}
	return recorded, nil
}

func (r *release) planWorkersOfLastRelease(ctx context.Context, app provider.StackRef, progress progress.Log) ([]provider.ChangeGroup, error) {
	refs, err := r.listWorkersOfLastRelease(ctx, app)
	if err != nil {
		return nil, err
	}
	var groups []provider.ChangeGroup
	for _, ref := range refs {
		planned, err := r.automation.PreviewDestroy(ctx, ref, progress)
		if err != nil {
			return nil, err
		}
		groups = append(groups, planned.Groups...)
	}
	return groups, nil
}

func (r *release) destroyWorkersOfLastRelease(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	app := req.Ref
	refs, err := r.listWorkersOfLastRelease(ctx, app)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		say(progress, "Removing stack "+ref.Name.String())
		workers := req
		workers.Ref = ref
		if err := r.destroyWorkersStack(ctx, workers, progress); err != nil {
			return err
		}
	}
	return nil
}

func (r *Stacks) PlanWorkersRetirement(ctx context.Context, live provider.LiveApps, _ progress.Log) (provider.Plan, error) {
	opened, retired, err := r.listRetiredWorkers(ctx, live)
	if err != nil || opened == nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	for _, stack := range retired {
		plan.Groups = append(plan.Groups, provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   stack.ref.Name.String(),
			Action: provider.ActionDelete,
			Reason: stack.reason,
		})
	}
	return plan, nil
}

func (r *Stacks) RetireWorkers(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
	opened, retired, err := r.listRetiredWorkers(ctx, live)
	if err != nil || opened == nil {
		return err
	}
	for _, stack := range retired {
		say(progress, fmt.Sprintf("Removing stack %s: %s", stack.ref.Name, stack.reason))
		if err := opened.destroyWorkersStack(ctx, provider.StackDestroy{Ref: stack.ref, Lease: live.Lease}, progress); err != nil {
			return err
		}
	}
	return nil
}

type retiredWorkersStack struct {
	ref    provider.StackRef
	reason string
}

func (r *Stacks) listRetiredWorkers(ctx context.Context, live provider.LiveApps) (*release, []retiredWorkersStack, error) {
	infra := provider.StackRef{Project: live.Slug, Tier: live.Tier, Name: naming.InfraStack(live.Env)}
	opened, err := r.at(ctx, infra, "")
	if err != nil || opened.cfg.KeyValues == nil {
		return nil, nil, err
	}
	recorded, err := opened.cfg.KeyValues.List(ctx, stackrecords.StacksPartition(live.Tier, live.Slug), workersKey, live.Env)
	if err != nil {
		return nil, nil, err
	}
	var retired []retiredWorkersStack
	for _, entry := range recorded {
		stack, found := recordedWorkersStack(entry.Key, live.Env)
		if !found {
			continue
		}
		reason := fmt.Sprintf("app %s is no longer deployed to %s", stack.App, live.Env)
		if deployed, found := live.Find(stack.App); found {
			if needsWorkersStack(deployed, stack) {
				continue
			}
			reason = fmt.Sprintf("app %s hosts no workers on %s any more", stack.App, workersCompute(stack))
		}
		ref := provider.StackRef{Project: live.Slug, Tier: live.Tier, Name: stack}
		retired = append(retired, retiredWorkersStack{ref: ref, reason: reason})
	}
	return opened, retired, nil
}

func recordedWorkersStack(key records.Key, env string) (naming.StackName, bool) {
	rest, found := key.Under(workersKey, env)
	switch {
	case found && len(rest) == 1:
		return worker.StackName(env, rest[0]), true
	case found && len(rest) == 2 && rest[1] == containerWorkersKey:
		return naming.ContainerWorkersStack(env, rest[0]), true
	}
	return naming.StackName{}, false
}

func workersCompute(stack naming.StackName) string {
	if stack.HoldsContainerWorkers() {
		return "containers"
	}
	return "functions"
}

func say(progress progress.Log, line string) {
	if progress != nil {
		progress.Say(line)
	}
}

func hostsContainerWorkers(spec provider.StackSpec) bool {
	return spec.App != nil && len(spec.App.Workers) > 0 && spec.Ref.Name.Env != ""
}

func (r *release) previewContainerWorkers(ctx context.Context, spec provider.StackSpec, web *container.Definition, progress progress.Log) ([]provider.ChangeGroup, error) {
	if !hostsContainerWorkers(spec) {
		return nil, nil
	}
	hosted, err := worker.NewContainerStack(spec, web, r.cfg.workerTarget())
	if err != nil {
		return nil, err
	}
	ref := containerWorkersRef(spec.Ref)
	planned, err := r.automation.Preview(ctx, ref, component.Program{Run: hosted.Declare, Tags: spec.Tags}, spec.Lease, progress)
	return planned.Groups, err
}

func (r *release) provisionContainerWorkers(ctx context.Context, spec provider.StackSpec, web *container.Definition, progress progress.Log) ([]provider.AppContainer, error) {
	if !hostsContainerWorkers(spec) {
		return nil, nil
	}
	hosted, err := worker.NewContainerStack(spec, web, r.cfg.workerTarget())
	if err != nil {
		return nil, err
	}
	ref := containerWorkersRef(spec.Ref)
	if err := r.recordWorkers(ctx, ref); err != nil {
		return nil, fmt.Errorf("record the workers of %s: %w", spec.App.App, err)
	}
	if r.cfg.KeyValues != nil {
		if err := r.containers.RecordConsumer(ctx, ref); err != nil {
			return nil, err
		}
	}
	outputs, err := r.automation.Run(ctx, ref, component.Program{Run: hosted.Declare, Tags: spec.Tags}, spec.Lease, progress)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	result, err := hosted.Decode(outputs)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	return result.Containers, nil
}
