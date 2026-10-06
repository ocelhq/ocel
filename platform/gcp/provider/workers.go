package gcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

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

const pushCeiling = 600 * time.Second

var workerCeilings = []provider.WorkerCeiling{
	{Compute: provider.ComputeServerless, MaxDuration: pushCeiling},
	{Compute: provider.ComputeContainer, MaxDuration: pushCeiling},
}

func workerServing(worker provider.WorkerSpec, placed serving) serving {
	env := maps.Clone(placed.env)
	if env == nil {
		env = map[string]string{}
	}
	env[processenv.WorkerEnvVar] = worker.Name
	placed.env = env
	placed.compute, placed.ingress, placed.timeout, placed.concurrency = provider.ComputeServerless, ingressInternal, pushCeiling, worker.Concurrency
	return placed
}

func publishURL(c *clients) string {
	if c.emulated() {
		return c.endpoint
	}
	return topics.PublishURL
}

func tasksManifest(c *clients, ref provider.StackRef, app string, declared map[string]*provider.TopicSpec) *live.Tasks {
	pinned := make(map[string]provider.TopicSpec, len(declared))
	for name, spec := range declared {
		pinned[name] = *spec
	}
	return &live.Tasks{
		Environment: ref.Name.Env,
		Topics:      pinned,
		DelayQueue:  c.DelayQueuePath(c.region, ref.Tier),
		Account:     c.AppAccountEmail(ref.Tier, ref.Project, app),
		PublishURL:  publishURL(c),
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

func runsTopics(app *provider.AppSpec) bool {
	return app.PreviewLabel == ""
}

func reachesTopics(app *provider.AppSpec) bool {
	return hostsWorkers(app) || runsTopics(app) && slices.ContainsFunc(app.Values.Bindings, func(binding provider.Binding) bool {
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
	return tasksManifest(c, spec.Ref, spec.App.App, declared), declared, nil
}

type deployedWorker struct {
	name     string
	service  string
	url      string
	revision string
}

func hostsWorkers(app *provider.AppSpec) bool {
	return len(app.Workers) > 0 && runsTopics(app)
}

func nameWorkers(names Names, spec provider.StackSpec) []deployedWorker {
	if !hostsWorkers(spec.App) {
		return nil
	}
	named := make([]deployedWorker, 0, len(spec.App.Workers))
	for _, worker := range spec.App.Workers {
		named = append(named, deployedWorker{name: worker.Name, service: names.WorkerService(spec.Ref.Project, spec.Ref.Name.Env, spec.App.App, worker.Name)})
	}
	return named
}

func (p *Provider) provisionWorkers(ctx context.Context, c *clients, spec provider.StackSpec, image, account string, env map[string]string,
	declared map[string]*provider.TopicSpec, progress progress.Log,
) ([]deployedWorker, error) {
	named := nameWorkers(c.Names, spec)
	if len(named) == 0 {
		return nil, nil
	}
	pushAccount := c.PushAccountEmail(spec.Ref.Tier)
	pushes := map[string]topics.Push{}
	deployed := make([]deployedWorker, 0, len(named))
	for at, worker := range named {
		ran, err := p.deployService(ctx, workerServing(spec.App.Workers[at], serving{
			service: worker.service,
			image:   image,
			env:     env,
			account: account,
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
	agent, err := c.ReadServiceAgent(ctx, pubSubAgentDomain)
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
