package vps

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	workersGroupKind    = "workers"
	workerContainerKind = "worker container"
	retiredAppTemplate  = "app %s is no longer deployed to %s"
	droppedTemplate     = "app %s no longer hosts these workers"
)

func retiredReason(apps provider.LiveApps, name string, worker boxapi.QueueWorker) (string, bool) {
	deployed, found := apps.Find(worker.App)
	if !found {
		return fmt.Sprintf(retiredAppTemplate, worker.App, apps.Env), true
	}
	if slices.Contains(deployed.Workers, name) {
		return "", false
	}
	return fmt.Sprintf(droppedTemplate, worker.App), true
}

func (p *Provider) PlanRetirement(ctx context.Context, apps provider.LiveApps, _ progress.Log) (provider.Plan, error) {
	partition, under := boxapi.QueueWorkersUnder(apps.Tier, apps.Slug, apps.Env)
	entries, err := p.keyValues.List(ctx, partition, under...)
	if err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	for _, entry := range entries {
		var worker boxapi.QueueWorker
		if err := json.Unmarshal(entry.Value, &worker); err != nil {
			continue
		}
		reason, retired := retiredReason(apps, entry.Key.Path[len(entry.Key.Path)-1], worker)
		if !retired {
			continue
		}
		at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Name == worker.App })
		if at < 0 {
			plan.Groups = append(plan.Groups, provider.ChangeGroup{
				Kind: workersGroupKind, Name: worker.App, Action: provider.ActionDelete, Reason: reason,
			})
			at = len(plan.Groups) - 1
		}
		plan.Groups[at].Changes = append(plan.Groups[at].Changes, provider.Change{Kind: workerContainerKind, Name: worker.Container, Action: provider.ActionDelete})
	}
	return plan, nil
}

func (p *Provider) Retire(ctx context.Context, apps provider.LiveApps, progress progress.Log) error {
	ref := provider.StackRef{Project: apps.Slug, Tier: apps.Tier, Name: naming.StackName{Env: apps.Env}}
	return p.retireWorkers(ctx, ref, func(name string, worker boxapi.QueueWorker) bool {
		reason, retired := retiredReason(apps, name, worker)
		if retired {
			progress.Say(fmt.Sprintf("Taking down worker %s's container %s: %s", name, worker.Container, reason))
		}
		return retired
	})
}
