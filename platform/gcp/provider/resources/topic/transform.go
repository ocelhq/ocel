package topic

import (
	"cmp"
	"context"
	"maps"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func transformTargets() map[provider.BindingType]*component.Transform {
	topic := surface.Allowed{
		topics.TopicSurface.Name:        topics.TopicSurface.Paths(),
		topics.SubscriptionSurface.Name: topics.SubscriptionSurface.Paths(),
	}
	task := maps.Clone(topic)
	task[topics.JobSurface.Name] = topics.JobSurface.Paths()
	return map[provider.BindingType]*component.Transform{
		provider.BindingTopic: {Kind: string(provider.BindingTopic), Allowed: topic},
		provider.BindingTask:  {Kind: string(provider.BindingTask), Allowed: task},
	}
}

func (p *Topic) askedOf(ctx context.Context, in resources.ProvisionRequest, topic declaredTopic) (map[string]topics.Asked, error) {
	evaluated, _ := surface.ReadEvaluated(ctx, in.Ref)
	return map[string]topics.Asked{topic.declared: {
		Resource: string(in.Resource.Type) + " " + topic.declared,
		Result:   evaluated.For(string(in.Resource.Type), in.Resource.Name),
	}}, nil
}

type retentionProperties struct {
	topic               map[string]any
	topicUnknown        []string
	subscription        map[string]any
	subscriptionUnknown []string
}

func pricedRetention(inventory component.Inventory, resource provider.Resource) (retentionProperties, error) {
	result := inventory.Transforms.For(string(resource.Type), resource.Name)
	named := string(resource.Type) + " " + resource.Name
	known, topicUnknown := topics.TopicSurface.SplitOutputs(result.Patches[topics.TopicSurface.Name])
	topic, err := topics.TopicSurface.Patch(named, pubsub.Topic{}, known, nil, surface.Recorded{})
	if err != nil {
		return retentionProperties{}, err
	}
	known, subscriptionUnknown := topics.SubscriptionSurface.SplitOutputs(result.Patches[topics.SubscriptionSurface.Name])
	subscription, err := topics.SubscriptionSurface.Patch(named, pubsub.Subscription{}, known, nil, surface.Recorded{})
	if err != nil {
		return retentionProperties{}, err
	}
	retained := retentionProperties{
		topic: map[string]any{},
		subscription: map[string]any{
			"message_retention_duration": cmp.Or(subscription.Desired.MessageRetentionDuration, topics.DefaultSubscriptionRetention),
			"retain_acked_messages":      subscription.Desired.RetainAckedMessages,
		},
		topicUnknown:        surface.ListPriced(topicUnknown, pricedPaths),
		subscriptionUnknown: surface.ListPriced(subscriptionUnknown, pricedPaths),
	}
	if retention := topic.Desired.MessageRetentionDuration; retention != "" {
		retained.topic["message_retention_duration"] = retention
	}
	return retained, nil
}

var pricedPaths = map[string]string{
	"messageRetentionDuration": "message_retention_duration",
	"retainAckedMessages":      "retain_acked_messages",
}
