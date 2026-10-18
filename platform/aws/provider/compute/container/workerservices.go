package container

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

func WorkerServiceName(project, env, app, worker string) string {
	return component.AppScopedName(naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindWorker, Name: worker})
}

func WorkerServices(tier environment.Tier, project, env string, workers []provider.WorkerSpec) map[string]queues.Worker {
	served := map[string]queues.Worker{}
	for _, worker := range workers {
		if worker.Compute != provider.ComputeContainer {
			continue
		}
		served[worker.Name] = queues.Worker{
			Concurrency: worker.Concurrency,
			Service:     &queues.Service{Cluster: awsports.ContainerClusterName(tier), Name: WorkerServiceName(project, env, worker.App, worker.Name)},
		}
	}
	if len(served) == 0 {
		return nil
	}
	return served
}

func WorkerServiceARNs(table topic.StateTable, tier environment.Tier, project, env string, workers []provider.WorkerSpec) []string {
	var arns []string
	for _, worker := range WorkerServices(tier, project, env, workers) {
		arns = append(arns, fmt.Sprintf("arn:aws:ecs:%s:%s:service/%s/%s", table.Region, component.AccountOfARN(table.ARN), worker.Service.Cluster, worker.Service.Name))
	}
	slices.Sort(arns)
	return arns
}
