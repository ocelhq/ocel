package gcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

func taskNames(names Names, ref provider.StackRef) topics.Names {
	return topics.Names{
		Namespace: names.namespace,
		Scope:     topics.Scope{Slug: ref.Project, Tier: ref.Tier, Environment: ref.Name.Env},
	}
}

func topologyOf(c *clients, ref provider.StackRef, declared map[string]*provider.TopicSpec) topics.Topology {
	return topics.Topology{Names: taskNames(c.Names, ref), Topics: declared}
}

func (p *Provider) ProvisionTopic(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	topic := declaredTopicOf(in.Resource)
	topology := topologyOf(c, in.Ref, map[string]*provider.TopicSpec{topic.declared: topic.spec})
	path := topics.TopicPath(c.project, topology.Names.Topic(topic.declared))
	ensureProgress(progress).Say(fmt.Sprintf("Provisioning %s %s as Pub/Sub topic %s with %d consumer(s)", in.Resource.Type, topic.declared, path, len(topic.spec.Consumers)))
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

func (p *Provider) PlanTopic(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return "", "", err
	}
	topic := declaredTopicOf(in.Resource)
	topology := topologyOf(c, in.Ref, map[string]*provider.TopicSpec{topic.declared: topic.spec})
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

func (p *Provider) recordedTopic(ctx context.Context, ref provider.StackRef, resource provider.Resource) (declaredTopic, bool, error) {
	stack, recorded, err := stackrecords.Read(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name)
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

func (p *Provider) RemoveTopic(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	topic, recorded, err := readDeclaredTopic(binding)
	if err != nil || !recorded {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	ensureProgress(progress).Say(fmt.Sprintf("Removing %s %s and its consumers' subscriptions", binding.Type, topic.declared))
	return topologyOf(c, ref, map[string]*provider.TopicSpec{topic.declared: topic.spec}).Remove(ctx, c.Workload())
}
