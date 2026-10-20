package realtime

import (
	"context"
	"fmt"
	"net/http"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const topicSubscriberRole = "roles/pubsub.subscriber"

func (r realtimeEnvironment) topic() string { return topics.TopicPath(r.clients.Project(), r.gateway) }

func (r realtimeEnvironment) topicGrants() policy.Change {
	member := realtimeMember(r.clients, r.ref.Tier)
	return policy.Each(
		policy.Grant(topics.PublisherRole, member, nil),
		policy.Grant(topicSubscriberRole, member, nil),
	)
}

func (r realtimeEnvironment) topicPolicy() (*pubsub.Service, policy.Resource, error) {
	service, err := r.clients.Workload().PubSub()
	if err != nil {
		return nil, policy.Resource{}, err
	}
	return service, topics.TopicPolicy(service, r.topic()), nil
}

func (r realtimeEnvironment) readTopicDifference(ctx context.Context) (string, error) {
	_, resource, err := r.topicPolicy()
	if err != nil {
		return "", err
	}
	bindings, err := policy.Read(ctx, resource)
	if policy.IsAbsent(err) {
		return "the realtime topic " + r.topic() + " is created for the gateway's instances to hear each other's publishes through", nil
	}
	if err != nil {
		return "", err
	}
	if _, differs := r.topicGrants()(bindings); differs {
		return "the realtime account " + r.clients.RealtimeAccountEmail(r.ref.Tier) + " is let publish on and subscribe to the topic " + r.topic() + " again", nil
	}
	return "", nil
}

func (r realtimeEnvironment) ensureTopic(ctx context.Context) error {
	service, resource, err := r.topicPolicy()
	if err != nil {
		return err
	}
	_, err = policy.Edit(ctx, resource, r.topicGrants())
	if !policy.IsAbsent(err) {
		return err
	}
	labels, err := names.StackLabels(r.clients.Names, r.ref)
	if err != nil {
		return err
	}
	topic := &pubsub.Topic{Labels: labels, Tags: r.clients.InlineTierTags(r.ref.Tier)}
	_, err = googleretry.Attempted(ctx, service.Projects.Topics.Create(r.topic(), topic).Context(ctx).Do)
	if err != nil && googleretry.AnsweredCode(err) != http.StatusConflict {
		return fmt.Errorf("create the realtime topic %s: %w", r.topic(), err)
	}
	_, err = policy.Edit(ctx, resource, r.topicGrants())
	return err
}

func (r realtimeEnvironment) removeTopic(ctx context.Context) error {
	service, err := r.clients.Workload().PubSub()
	if err != nil {
		return err
	}
	if err := r.clients.RemoveTierTagIfFound(ctx, r.ref.Tier, func(ctx context.Context) (gcloud.TaggedResource, bool, error) {
		_, err := googleretry.Attempted(ctx, service.Projects.Topics.Get(r.topic()).Context(ctx).Do)
		if googleretry.AnsweredCode(err) == http.StatusNotFound {
			return gcloud.TaggedResource{}, false, nil
		}
		if err != nil {
			return gcloud.TaggedResource{}, false, fmt.Errorf("read the realtime topic %s: %w", r.topic(), err)
		}
		return r.clients.PubSubTagged(r.topic()), true, nil
	}); err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Topics.Delete(r.topic()).Context(ctx).Do)
	if err != nil && googleretry.AnsweredCode(err) != http.StatusNotFound {
		return fmt.Errorf("delete the realtime topic %s: %w", r.topic(), err)
	}
	return nil
}
