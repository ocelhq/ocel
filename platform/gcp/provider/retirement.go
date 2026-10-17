package gcp

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	workersGroupKind   = "workers"
	runServiceKind     = "run.Service"
	retiredAppTemplate = "app %s is no longer deployed to %s"
)

type retiredWorker struct {
	app     string
	service string
}

func (p *Provider) PlanRetirement(ctx context.Context, live provider.LiveApps, _ progress.Log) (provider.Plan, error) {
	retired, err := p.listRetiredWorkers(ctx, live)
	if err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	for _, worker := range retired {
		at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Name == worker.app })
		if at < 0 {
			plan.Groups = append(plan.Groups, provider.ChangeGroup{
				Kind: workersGroupKind, Name: worker.app, Action: provider.ActionDelete, Reason: fmt.Sprintf(retiredAppTemplate, worker.app, live.Env),
			})
			at = len(plan.Groups) - 1
		}
		plan.Groups[at].Changes = append(plan.Groups[at].Changes, provider.Change{Kind: runServiceKind, Name: worker.service, Action: provider.ActionDelete})
	}
	return plan, nil
}

func (p *Provider) Retire(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
	retired, err := p.listRetiredWorkers(ctx, live)
	if err != nil {
		return err
	}
	for _, worker := range retired {
		ensureProgress(progress).Say(fmt.Sprintf("Retiring worker service %s: "+retiredAppTemplate, worker.service, worker.app, live.Env))
		if _, err := p.deleteService(ctx, worker.service, progress); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) listRetiredWorkers(ctx context.Context, live provider.LiveApps) ([]retiredWorker, error) {
	entries, err := stackrecords.List(ctx, p.KeyValues(), live.Tier, live.Slug)
	if err != nil {
		return nil, err
	}
	var recorded []retiredWorker
	for _, entry := range entries {
		if entry.Name.IsInfra() || entry.Name.Env != live.Env || live.Has(entry.Name.App) {
			continue
		}
		for _, physical := range recordedWorkerServices(entry.Stack) {
			worker := retiredWorker{app: entry.Name.App, service: physical}
			if !slices.Contains(recorded, worker) {
				recorded = append(recorded, worker)
			}
		}
	}
	if len(recorded) == 0 {
		return nil, nil
	}
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return nil, err
	}
	var retired []retiredWorker
	for _, worker := range recorded {
		_, err := p.read(ctx, services, clients.servicePath(worker.service), worker.service)
		if absent(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		retired = append(retired, worker)
	}
	return retired, nil
}

func recordedWorkerServices(stack stackrecords.Stack) []string {
	var services []string
	for _, function := range stack.Functions {
		if _, isWorker := resources.WorkerOf(function.Name); isWorker && function.Physical != "" {
			services = append(services, function.Physical)
		}
	}
	for _, container := range stack.Containers {
		if _, isWorker := resources.WorkerOf(container.Name); isWorker && container.Physical != "" {
			services = append(services, container.Physical)
		}
	}
	return services
}
