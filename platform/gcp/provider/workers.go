package gcp

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const invokerRole = "roles/run.invoker"

type workerPlacement struct {
	service string
	image   string
	env     map[string]string
	account string
	worker  provider.WorkerSpec
	egress  *privateEgress
}

func workerServing(placed workerPlacement) serving {
	env := maps.Clone(placed.env)
	if env == nil {
		env = map[string]string{}
	}
	env[processenv.WorkerEnvVar] = placed.worker.Name
	return serving{
		service:     placed.service,
		image:       placed.image,
		env:         env,
		account:     placed.account,
		compute:     provider.ComputeServerless,
		ingress:     ingressInternal,
		timeout:     pushCeiling,
		concurrency: placed.worker.Concurrency,
		egress:      placed.egress,
	}
}

func publishURL(c *clients) string {
	if c.emulated() {
		return c.endpoint
	}
	return topics.PublishURL
}

func tasksManifest(c *clients, ref provider.StackRef, declared map[string]*provider.TopicSpec) *live.Tasks {
	pinned := make(map[string]provider.TopicSpec, len(declared))
	for name, spec := range declared {
		pinned[name] = *spec
	}
	return &live.Tasks{
		Environment:  ref.Name.Env,
		Topics:       pinned,
		DelayQueue:   c.DelayQueuePath(c.region, ref.Tier),
		DelayAccount: c.WorkloadAccountEmail(ref.Tier),
		PublishURL:   publishURL(c),
	}
}

func (p *Provider) declaredTopics(ctx context.Context, ref provider.StackRef) (map[string]*provider.TopicSpec, error) {
	stack, recorded, err := stackrecords.Read(ctx, p.KeyValues(), ref.Tier, ref.Project, naming.InfraStack(ref.Name.Env))
	if err != nil || !recorded {
		return nil, err
	}
	declared := map[string]*provider.TopicSpec{}
	for _, binding := range stack.Bindings {
		topic, isTopic, err := readDeclaredTopic(binding)
		if err != nil {
			return nil, err
		}
		if isTopic {
			declared[topic.declared] = topic.spec
		}
	}
	return declared, nil
}

func reachesTopics(app *provider.AppSpec) bool {
	if app.PreviewLabel != "" {
		return false
	}
	return len(app.Workers) > 0 || slices.ContainsFunc(app.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingTopic || binding.Type == provider.BindingTask
	})
}

func (p *Provider) tasksFor(ctx context.Context, c *clients, spec provider.StackSpec) (*live.Tasks, map[string]*provider.TopicSpec, error) {
	if !reachesTopics(spec.App) {
		return nil, nil, nil
	}
	declared, err := p.declaredTopics(ctx, spec.Ref)
	if err != nil {
		return nil, nil, err
	}
	return tasksManifest(c, spec.Ref, declared), declared, nil
}

type deployedWorker struct {
	name     string
	service  string
	url      string
	revision string
}

func hostsWorkers(app *provider.AppSpec) bool {
	return len(app.Workers) > 0 && app.PreviewLabel == ""
}

func (p *Provider) nameWorkers(c *clients, spec provider.StackSpec) ([]deployedWorker, error) {
	if !hostsWorkers(spec.App) {
		return nil, nil
	}
	named := make([]deployedWorker, 0, len(spec.App.Workers))
	for _, worker := range spec.App.Workers {
		service, err := c.WorkerService(spec.Ref.Project, spec.Ref.Name.Env, spec.App.App, worker.Name)
		if err != nil {
			return nil, err
		}
		named = append(named, deployedWorker{name: worker.Name, service: service})
	}
	return named, nil
}

func (p *Provider) provisionWorkers(ctx context.Context, c *clients, spec provider.StackSpec, image string, env map[string]string,
	declared map[string]*provider.TopicSpec, progress progress.Log,
) ([]deployedWorker, error) {
	named, err := p.nameWorkers(c, spec)
	if err != nil || len(named) == 0 {
		return nil, err
	}
	pushAccount := c.PushAccountEmail(spec.Ref.Tier)
	pushes := map[string]topics.Push{}
	deployed := make([]deployedWorker, 0, len(named))
	for at, worker := range named {
		ran, err := p.deployService(ctx, workerServing(workerPlacement{
			service: worker.service,
			image:   image,
			env:     env,
			account: c.WorkloadAccountEmail(spec.Ref.Tier),
			worker:  spec.App.Workers[at],
			egress:  p.egressFor(c.Names, spec),
		}), progress)
		if err != nil {
			return nil, err
		}
		if err := p.Pin(ctx, worker.service, ran.revision, nil); err != nil {
			return nil, err
		}
		if err := p.grantInvoker(ctx, c, worker.service, "serviceAccount:"+pushAccount); err != nil {
			return nil, err
		}
		pushes[worker.name] = topics.Push{URL: ran.url, ServiceAccount: pushAccount}
		worker.url, worker.revision = ran.url, ran.revision
		deployed = append(deployed, worker)
	}
	agent, err := c.ServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return nil, err
	}
	subscriptions := topics.Subscriptions{Names: taskNames(c.Names, spec.Ref), Topics: declared, Pushes: pushes, Agent: agent}
	if err := subscriptions.Ensure(ctx, c.Workload()); err != nil {
		return nil, err
	}
	return deployed, nil
}

func workerFunctions(workers []deployedWorker) []provider.Function {
	functions := make([]provider.Function, 0, len(workers))
	for _, worker := range workers {
		functions = append(functions, provider.Function{Name: resources.WorkerName(worker.name), Physical: worker.service, URL: worker.url, Revision: worker.revision})
	}
	return functions
}

func workerContainers(workers []deployedWorker, image string) []provider.AppContainer {
	containers := make([]provider.AppContainer, 0, len(workers))
	for _, worker := range workers {
		containers = append(containers, provider.AppContainer{Name: resources.WorkerName(worker.name), Physical: worker.service, URL: worker.url, Image: image, Revision: worker.revision})
	}
	return containers
}

func (p *Provider) grantInvoker(ctx context.Context, c *clients, service, member string) error {
	services, err := c.Run()
	if err != nil {
		return err
	}
	path := c.servicePath(service)
	return p.retryWrite(ctx, "let "+member+" invoke "+service, func() error {
		policy, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.GetIamPolicy(path).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("read who may invoke %s: %w", service, err)
		}
		if slices.ContainsFunc(policy.Bindings, func(binding *run.GoogleIamV1Binding) bool {
			return binding.Role == invokerRole && binding.Condition == nil && slices.Contains(binding.Members, member)
		}) {
			return nil
		}
		policy.Bindings = append(policy.Bindings, &run.GoogleIamV1Binding{Role: invokerRole, Members: []string{member}})
		_, err = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.SetIamPolicy(path, &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(call...)
		})
		return err
	})
}
