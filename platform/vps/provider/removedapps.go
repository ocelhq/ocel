package vps

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	workersGroupKind    = "workers"
	workerContainerKind = "worker container"
	retiredAppTemplate  = "app %s is no longer deployed to %s"
)

func (p *Provider) PlanRemovedApps(ctx context.Context, apps provider.LiveApps, _ progress.Log) (provider.Plan, error) {
	partition, under := live.QueueWorkersUnder(apps.Tier, apps.Slug, apps.Env)
	entries, err := p.keyValues.List(ctx, partition, under...)
	if err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	for _, entry := range entries {
		var worker live.QueueWorker
		if err := json.Unmarshal(entry.Value, &worker); err != nil || slices.Contains(apps.Apps, worker.App) {
			continue
		}
		at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Name == worker.App })
		if at < 0 {
			plan.Groups = append(plan.Groups, provider.ChangeGroup{
				Kind: workersGroupKind, Name: worker.App, Action: provider.ActionDelete, Reason: fmt.Sprintf(retiredAppTemplate, worker.App, apps.Env),
			})
			at = len(plan.Groups) - 1
		}
		plan.Groups[at].Changes = append(plan.Groups[at].Changes, provider.Change{Kind: workerContainerKind, Name: worker.Container, Action: provider.ActionDelete})
	}
	return plan, nil
}

func (p *Provider) RetireRemovedApps(ctx context.Context, apps provider.LiveApps, progress progress.Log) error {
	ref := provider.StackRef{Project: apps.Slug, Tier: apps.Tier, Name: naming.StackName{Env: apps.Env}}
	return p.retireWorkers(ctx, ref, func(name string, worker live.QueueWorker) bool {
		if slices.Contains(apps.Apps, worker.App) {
			return false
		}
		if progress != nil {
			progress.Say(fmt.Sprintf("Taking down worker %s's container %s: "+retiredAppTemplate, name, worker.Container, worker.App, apps.Env))
		}
		return true
	})
}
