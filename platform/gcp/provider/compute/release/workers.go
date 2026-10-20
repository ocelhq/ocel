package release

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

var WorkerCeilings = []provider.WorkerCeiling{
	{Compute: provider.ComputeServerless, MaxDuration: topic.PushCeiling},
	{Compute: provider.ComputeContainer, MaxDuration: topic.PushCeiling},
}

func WorkerServing(worker provider.WorkerSpec, placed run.Serving) run.Serving {
	env := maps.Clone(placed.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[processenv.WorkerEnvVar] = worker.Name
	placed.Env = env
	placed.Compute, placed.Ingress, placed.Timeout, placed.Concurrency = provider.ComputeServerless, run.IngressInternal, topic.PushCeiling, worker.Concurrency
	return placed
}

type deployedWorker struct {
	name     string
	service  string
	url      string
	revision string
}

func NameWorkers(names names.Names, spec provider.StackSpec) ([]deployedWorker, error) {
	if !topic.HostsWorkers(spec.App) {
		return nil, nil
	}
	named := make([]deployedWorker, 0, len(spec.App.Workers))
	for _, worker := range spec.App.Workers {
		service, err := names.WorkerService(spec.Ref.Project, spec.Ref.Name.Env, spec.App.App, worker.Name)
		if err != nil {
			return nil, err
		}
		named = append(named, deployedWorker{name: worker.Name, service: service})
	}
	return named, nil
}

func (p *Release) ProvisionWorkers(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, image, account string, env map[string]string,
	declared map[string]*provider.TopicSpec, instances provider.Instances, progress progress.Log,
) ([]deployedWorker, error) {
	named, err := NameWorkers(c.Names, spec)
	if err != nil {
		return nil, err
	}
	if len(named) == 0 {
		return nil, nil
	}
	pushAccount := c.PushAccountEmail(spec.Ref.Tier)
	pushes := map[string]topics.Push{}
	labels, err := names.StackLabels(c.Names, spec.Ref)
	if err != nil {
		return nil, err
	}
	deployed := make([]deployedWorker, 0, len(named))
	for at, worker := range named {
		ran, err := p.Services.DeployService(ctx, WorkerServing(spec.App.Workers[at], run.Serving{
			Service:   worker.service,
			Tier:      spec.Ref.Tier,
			Image:     image,
			Env:       env,
			Account:   account,
			Egress:    p.EgressFor(c.Names, spec),
			Labels:    labels,
			Instances: instances,
		}), progress)
		if err != nil {
			return nil, err
		}
		if _, err := p.Services.Pin(ctx, worker.service, ran.Revision, nil); err != nil {
			return nil, err
		}
		if err := run.GrantInvoker(ctx, c, worker.service, "serviceAccount:"+pushAccount); err != nil {
			return nil, err
		}
		pushes[worker.name] = topics.Push{URL: ran.Url, ServiceAccount: pushAccount}
		worker.url, worker.revision = ran.Url, ran.Revision
		deployed = append(deployed, worker)
	}
	subscriptions := topics.Subscriptions{Names: topic.Names(c.Names, spec.Ref), Topics: declared, Pushes: pushes, Tags: topic.TierTagsOf(c, spec.Ref.Tier)}
	if err := subscriptions.Ensure(ctx, c.Workload()); err != nil {
		return nil, err
	}
	return deployed, nil
}

func WorkerFunctions(workers []deployedWorker) []provider.Function {
	functions := make([]provider.Function, 0, len(workers))
	for _, worker := range workers {
		functions = append(functions, provider.Function{Name: resources.WorkerName(worker.name), Physical: worker.service, URL: worker.url, Revision: worker.revision})
	}
	return functions
}

func WorkerContainers(workers []deployedWorker, image string) []provider.AppContainer {
	containers := make([]provider.AppContainer, 0, len(workers))
	for _, worker := range workers {
		containers = append(containers, provider.AppContainer{Name: resources.WorkerName(worker.name), Physical: worker.service, URL: worker.url, Image: image, Revision: worker.revision})
	}
	return containers
}
