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

func WorkerServiceName(namespace, project, env, app, worker string) (string, error) {
	return component.PhysicalName(namespace, naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindWorker, Name: worker}, maxServiceNameLen)
}

func WorkerServices(namespace string, tier environment.Tier, project, env string, workers []provider.WorkerSpec) (map[string]queues.Worker, error) {
	served := map[string]queues.Worker{}
	for _, worker := range workers {
		if worker.Compute != provider.ComputeContainer {
			continue
		}
		name, err := WorkerServiceName(namespace, project, env, worker.App, worker.Name)
		if err != nil {
			return nil, err
		}
		served[worker.Name] = queues.Worker{
			Concurrency: worker.Concurrency,
			Service:     &queues.Service{Cluster: awsports.ContainerClusterName(namespace, tier), Name: name},
		}
	}
	if len(served) == 0 {
		return nil, nil
	}
	return served, nil
}

func WorkerServiceARNs(table topic.StateTable, namespace string, tier environment.Tier, project, env string, workers []provider.WorkerSpec) ([]string, error) {
	served, err := WorkerServices(namespace, tier, project, env, workers)
	if err != nil {
		return nil, err
	}
	var arns []string
	for _, worker := range served {
		arns = append(arns, fmt.Sprintf("arn:aws:ecs:%s:%s:service/%s/%s", table.Region, component.AccountOfARN(table.ARN), worker.Service.Cluster, worker.Service.Name))
	}
	slices.Sort(arns)
	return arns, nil
}
