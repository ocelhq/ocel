package topic

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type declaredTopic struct {
	declared string
	spec     *provider.TopicSpec
}

func declaredTopicOf(resource provider.Resource) declaredTopic {
	spec := resource.Topic
	if spec == nil {
		spec = &provider.TopicSpec{}
	}
	return declaredTopic{declared: cmp.Or(resource.Declared, resource.Name), spec: spec}
}

func readDeclaredTopic(binding provider.Binding) (declaredTopic, bool, error) {
	if binding.Type != provider.BindingTopic && binding.Type != provider.BindingTask {
		return declaredTopic{}, false, nil
	}
	raw, recorded := binding.Properties[stackrecords.PropertyTopicSpec]
	if !recorded {
		return declaredTopic{}, false, nil
	}
	var spec provider.TopicSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return declaredTopic{}, false, fmt.Errorf("read the %s %s's recorded config: %w", binding.Type, binding.Name, err)
	}
	return declaredTopic{declared: cmp.Or(binding.Properties[stackrecords.PropertyDeclared], binding.Name), spec: &spec}, true, nil
}

func Names(names names.Names, ref provider.StackRef) topics.Names {
	return topics.Names{
		Namespace: names.Namespace(),
		Scope:     topics.Scope{Slug: ref.Project, Tier: ref.Tier, Environment: ref.Name.Env},
	}
}

func nameTopic(names topics.Names, topic declaredTopic) (string, error) {
	for _, consumer := range topic.spec.Consumers {
		if err := topics.CheckConsumer(topic.declared, consumer.Name); err != nil {
			return "", err
		}
	}
	return names.Topic(topic.declared)
}

func topologyOf(c *gcloud.Clients, ref provider.StackRef, declared map[string]*provider.TopicSpec) topics.Topology {
	return topics.Topology{Names: Names(c.Names, ref), Topics: declared}
}

func (p *Topic) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	topic := declaredTopicOf(in.Resource)
	topology := topologyOf(c, in.Ref, map[string]*provider.TopicSpec{topic.declared: topic.spec})
	name, err := nameTopic(topology.Names, topic)
	if err != nil {
		return provider.Binding{}, err
	}
	path := topics.TopicPath(c.Project(), name)
	gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Provisioning %s %s as Pub/Sub topic %s with %d consumer(s)", in.Resource.Type, topic.declared, path, len(topic.spec.Consumers)))
	if previous, recorded, err := p.recordedTopic(ctx, in.Ref, in.Resource); err != nil {
		return provider.Binding{}, err
	} else if recorded {
		if err := refuseOrderingChange(previous, topic); err != nil {
			return provider.Binding{}, err
		}
		if err := topology.RemoveDropped(ctx, c.Workload(), map[string]*provider.TopicSpec{previous.declared: previous.spec}); err != nil {
			return provider.Binding{}, err
		}
	}
	if err := topology.Ensure(ctx, c.Workload()); err != nil {
		return provider.Binding{}, err
	}
	properties, err := encodeTopicProperties(topic)
	if err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:       in.Resource.Type,
		Name:       in.Resource.Name,
		Resource:   topic.declared,
		Properties: properties,
	}, nil
}

func (p *Topic) Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return "", "", err
	}
	topic := declaredTopicOf(in.Resource)
	topology := topologyOf(c, in.Ref, map[string]*provider.TopicSpec{topic.declared: topic.spec})
	if _, err := nameTopic(topology.Names, topic); err != nil {
		return "", "", err
	}
	var previous map[string]*provider.TopicSpec
	if recorded != nil {
		was, found, err := readDeclaredTopic(*recorded)
		if err != nil {
			return "", "", err
		}
		if found {
			if err := refuseOrderingChange(was, topic); err != nil {
				return "", "", err
			}
			previous = map[string]*provider.TopicSpec{was.declared: was.spec}
		}
	}
	missing, err := topology.FindMissing(ctx, c.Workload())
	if err != nil {
		return "", "", err
	}
	if len(missing) > 0 {
		return provider.ActionCreate, "", nil
	}
	differences, err := topology.ReadDifferences(ctx, c.Workload(), previous)
	if err != nil {
		return "", "", err
	}
	properties, err := encodeTopicProperties(topic)
	if err != nil {
		return "", "", err
	}
	if len(differences) == 0 && (recorded == nil || !isSameTopicRecord(recorded.Properties, properties)) {
		differences = append(differences, fmt.Sprintf("the config recorded for %s %s changes", in.Resource.Type, topic.declared))
	}
	if len(differences) > 0 {
		return provider.ActionUpdate, strings.Join(differences, "; "), nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func encodeTopicProperties(topic declaredTopic) (map[string]string, error) {
	spec, err := json.Marshal(topic.spec)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		stackrecords.PropertyDeclared:  topic.declared,
		stackrecords.PropertyTopicSpec: string(spec),
	}, nil
}

func isSameTopicRecord(recorded, properties map[string]string) bool {
	for name, value := range properties {
		if recorded[name] != value {
			return false
		}
	}
	return true
}

func refuseOrderingChange(previous, topic declaredTopic) error {
	if previous.spec.Ordered == topic.spec.Ordered {
		return nil
	}
	var kept []string
	for _, consumer := range topic.spec.Consumers {
		if slices.ContainsFunc(previous.spec.Consumers, func(was provider.ConsumerSpec) bool { return was.Name == consumer.Name }) {
			kept = append(kept, consumer.Name)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s now declares ordered %t, and its consumers %s were subscribed with ordered %t, which Pub/Sub cannot change on a subscription that exists.\n"+
			"Rename those consumers to start new subscriptions, or keep the topic's ordering",
		topic.declared, topic.spec.Ordered, strings.Join(kept, ", "), previous.spec.Ordered)
}

func (p *Topic) recordedTopic(ctx context.Context, ref provider.StackRef, resource provider.Resource) (declaredTopic, bool, error) {
	stack, recorded, err := stackrecords.Read(ctx, p.records, ref.Tier, ref.Project, ref.Name)
	if err != nil || !recorded {
		return declaredTopic{}, false, err
	}
	for _, binding := range stack.Bindings {
		if binding.Name == resource.Name && binding.Type == resource.Type {
			return readDeclaredTopic(binding)
		}
	}
	return declaredTopic{}, false, nil
}

func (p *Topic) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	topic, recorded, err := readDeclaredTopic(binding)
	if err != nil || !recorded {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Removing %s %s and its consumers' subscriptions", binding.Type, topic.declared))
	return topologyOf(c, ref, map[string]*provider.TopicSpec{topic.declared: topic.spec}).Remove(ctx, c.Workload())
}

type Topic struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
	records     records.Store
}

func New(openClients func(ctx context.Context) (*gcloud.Clients, error), store records.Store) *Topic {
	return &Topic{openClients: openClients, records: store}
}

func (p *Topic) Component() component.Component {
	return component.Component{
		Types:    []provider.BindingType{provider.BindingTopic, provider.BindingTask},
		Hooks:    resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		Shape:    shapeTopics,
		Accounts: []bootstrap.Account{{Name: names.Names.PushAccount, Purpose: task.PushPurpose}},
		Grants: func(ctx context.Context, app component.App) ([]grants.Grant, error) {
			if !ReachesTopics(app.Spec.App) {
				return nil, nil
			}
			return Grants(ctx, app.Clients, app.Spec, app.Account, app.Member, app.Topics)
		},
	}
}

func shapeTopics(shape component.Shape, declared []provider.Resource) error {
	req := shape.Request
	scope := Names(shape.Names, provider.StackRef{Project: req.Deploy.Slug, Tier: req.Deploy.Tier, Name: naming.InfraStack(req.Deploy.Env)})
	shape.Tree.AddShaped(shape.Shared, shape.Vendor, shape.Region, []pricing.Shaped{task.DelayQueueShape(shape.Names, req.Deploy.Tier, shape.Region)})
	for _, resource := range declared {
		topic := declaredTopicOf(resource)
		name, err := scope.Topic(topic.declared)
		if err != nil {
			return err
		}
		shape.Tree.Add(shape.Environment, shape.Vendor, cost.PubSubTopicType, name, shape.Region, map[string]any{})
		for _, consumer := range topic.spec.Consumers {
			subscription, err := scope.Subscription(topic.declared, consumer.Name)
			if err != nil {
				return err
			}
			shape.Tree.Add(shape.Environment, shape.Vendor, cost.PubSubSubscriptionType, subscription, shape.Region,
				map[string]any{"ack_deadline_seconds": int(PushCeiling.Seconds()), "enable_message_ordering": topic.spec.Ordered})
		}
		if topic.spec.Cron != "" {
			shape.Tree.Add(shape.Environment, shape.Vendor, cost.SchedulerJobType, name, shape.Region, map[string]any{"region": shape.Region, "schedule": topic.spec.Cron})
		}
	}
	return nil
}

const PushCeiling = 600 * time.Second

func publishURL(c *gcloud.Clients) string {
	if c.Emulated() {
		return c.Endpoint
	}
	return topics.PublishURL
}

func Manifest(c *gcloud.Clients, ref provider.StackRef, app string, declared map[string]*provider.TopicSpec) *live.Tasks {
	pinned := make(map[string]provider.TopicSpec, len(declared))
	for name, spec := range declared {
		pinned[name] = *spec
	}
	return &live.Tasks{
		Environment: ref.Name.Env,
		Topics:      pinned,
		DelayQueue:  c.DelayQueuePath(c.Region, ref.Tier),
		Account:     c.AppAccountEmail(ref.Project, ref.Name.Env, app),
		PublishURL:  publishURL(c),
	}
}

func (p *Topic) declaredTopics(ctx context.Context, ref provider.StackRef) (map[string]*provider.TopicSpec, error) {
	stack, recorded, err := stackrecords.Read(ctx, p.records, ref.Tier, ref.Project, naming.InfraStack(ref.Name.Env))
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

func ReachesTopics(app *provider.AppSpec) bool {
	return HostsWorkers(app) || slices.ContainsFunc(app.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingTopic || binding.Type == provider.BindingTask
	})
}

func (p *Topic) Tasks(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec) (*live.Tasks, map[string]*provider.TopicSpec, error) {
	if !ReachesTopics(spec.App) {
		return nil, nil, nil
	}
	declared, err := p.declaredTopics(ctx, spec.Ref)
	if err != nil {
		return nil, nil, err
	}
	return Manifest(c, spec.Ref, spec.App.App, declared), declared, nil
}

func Grants(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec) ([]grants.Grant, error) {
	agent, err := c.ReadServiceAgent(ctx, task.CloudTasksAgentDomain)
	if err != nil {
		return nil, err
	}
	granted := []grants.Grant{
		grants.OnProject(grants.TaskRecordsRole, member, variables.ProjectWritableDatabaseCondition(c, spec.Ref.Tier, spec.Ref.Project)),
		grants.On(grants.Queue, c.DelayQueuePath(c.Region, spec.Ref.Tier), task.QueueEnqueuerRole, member),
		grants.On(grants.Account, account, bootstrap.RunAsRole, member),
		grants.On(grants.Account, account, bootstrap.RunAsRole, agent),
	}
	names := Names(c.Names, spec.Ref)
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		topic, err := names.Topic(name)
		if err != nil {
			return nil, err
		}
		granted = append(granted, grants.On(grants.Topic, topics.TopicPath(c.Project(), topic), topics.PublisherRole, member))
	}
	return granted, nil
}

func DeclaresTopics(declared []provider.Resource) bool {
	return slices.ContainsFunc(declared, func(resource provider.Resource) bool {
		return resource.Type == provider.BindingTopic || resource.Type == provider.BindingTask
	})
}

func HostsWorkers(app *provider.AppSpec) bool {
	return len(app.Workers) > 0
}
