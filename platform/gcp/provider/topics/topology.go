package topics

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	pushAckDeadline         = 600 * time.Second
	maxBackoff              = 600 * time.Second
	deadLetterDeliveryLimit = 100
	mutableSubscription     = "pushConfig,retryPolicy,deadLetterPolicy,ackDeadlineSeconds"
)

type Push struct {
	URL            string
	ServiceAccount string
}

type Topology struct {
	Names  Names
	Topics map[string]*contractv1.ManifestTopic
	Pushes map[string]Push
}

func PushPath(topic, consumer string) string {
	return pushPrefix + topic + consumerInfix + consumer
}

func topicPath(project, topic string) string { return "projects/" + project + "/topics/" + topic }

func subscriptionPath(project, subscription string) string {
	return "projects/" + project + "/subscriptions/" + subscription
}

func (t Topology) Ensure(ctx context.Context, clients *ports.Clients) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		topic := t.Topics[name]
		if err := ensureTopic(ctx, service, topicPath(clients.Project, t.Names.Topic(name))); err != nil {
			return err
		}
		for _, consumer := range topic.GetConsumers() {
			push, placed := t.Pushes[consumer.GetWorker()]
			if !placed {
				return fmt.Errorf("consumer %s of %s runs on worker %s, and no push address was given for it", consumer.GetName(), name, consumer.GetWorker())
			}
			deadLetters := topicPath(clients.Project, t.Names.DeadLetterTopic(name, consumer.GetName()))
			if err := ensureTopic(ctx, service, deadLetters); err != nil {
				return err
			}
			subscription := t.subscription(clients.Project, name, topic, consumer, push, deadLetters)
			if err := ensureSubscription(ctx, service, subscriptionPath(clients.Project, t.Names.Subscription(name, consumer.GetName())), subscription); err != nil {
				return err
			}
		}
		if topic.GetCron() != "" && isTask(topic) {
			if err := t.ensureSchedule(ctx, clients, name, topic.GetCron()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t Topology) subscription(project, name string, topic *contractv1.ManifestTopic, consumer *contractv1.ManifestConsumer, push Push, deadLetters string) *pubsub.Subscription {
	policy := provider.ResolveRetryPolicy(topic.GetRetry(), consumer.GetRetry())
	return &pubsub.Subscription{
		Topic:                 topicPath(project, t.Names.Topic(name)),
		AckDeadlineSeconds:    int64(pushAckDeadline / time.Second),
		EnableMessageOrdering: topic.GetOrdered(),
		Filter:                fmt.Sprintf(`NOT attributes:%s OR attributes.%s = %q`, ConsumerAttribute, ConsumerAttribute, consumer.GetName()),
		PushConfig: &pubsub.PushConfig{
			PushEndpoint: push.URL + PushPath(name, consumer.GetName()),
			OidcToken:    &pubsub.OidcToken{ServiceAccountEmail: push.ServiceAccount, Audience: push.URL},
		},
		RetryPolicy: &pubsub.RetryPolicy{
			MinimumBackoff: durationText(min(policy.MinDelay, maxBackoff)),
			MaximumBackoff: durationText(min(policy.MaxDelay, maxBackoff)),
		},
		DeadLetterPolicy: &pubsub.DeadLetterPolicy{DeadLetterTopic: deadLetters, MaxDeliveryAttempts: deadLetterDeliveryLimit},
		ExpirationPolicy: &pubsub.ExpirationPolicy{},
	}
}

func durationText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

func ensureTopic(ctx context.Context, service *pubsub.Service, path string) error {
	err := retried(ctx, func() error {
		_, err := service.Projects.Topics.Create(path, &pubsub.Topic{}).Context(ctx).Do()
		return err
	})
	if err != nil && !isAnswered(err, http.StatusConflict) {
		return fmt.Errorf("create Pub/Sub topic %s: %w", path, err)
	}
	return nil
}

func ensureSubscription(ctx context.Context, service *pubsub.Service, path string, desired *pubsub.Subscription) error {
	err := retried(ctx, func() error {
		_, err := service.Projects.Subscriptions.Create(path, desired).Context(ctx).Do()
		return err
	})
	if err == nil {
		return nil
	}
	if !isAnswered(err, http.StatusConflict) {
		return fmt.Errorf("create Pub/Sub subscription %s: %w", path, err)
	}
	var current *pubsub.Subscription
	err = retried(ctx, func() error {
		var readErr error
		current, readErr = service.Projects.Subscriptions.Get(path).Context(ctx).Do()
		return readErr
	})
	if err != nil {
		return fmt.Errorf("read Pub/Sub subscription %s: %w", path, err)
	}
	if current.EnableMessageOrdering != desired.EnableMessageOrdering {
		return fmt.Errorf("subscription %s has message ordering %t and its topic now declares %t, and Pub/Sub cannot change ordering on a subscription that exists: rename the consumer to start a new subscription", path, current.EnableMessageOrdering, desired.EnableMessageOrdering)
	}
	err = retried(ctx, func() error {
		_, err := service.Projects.Subscriptions.Patch(path, &pubsub.UpdateSubscriptionRequest{Subscription: desired, UpdateMask: mutableSubscription}).Context(ctx).Do()
		return err
	})
	if err != nil {
		return fmt.Errorf("update Pub/Sub subscription %s: %w", path, err)
	}
	return nil
}

func (t Topology) Remove(ctx context.Context, clients *ports.Clients) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		for _, consumer := range t.Topics[name].GetConsumers() {
			subscription := subscriptionPath(clients.Project, t.Names.Subscription(name, consumer.GetName()))
			if err := deleteIgnoringMissing(ctx, "subscription "+subscription, func() error {
				_, err := service.Projects.Subscriptions.Delete(subscription).Context(ctx).Do()
				return err
			}); err != nil {
				return err
			}
			deadLetters := topicPath(clients.Project, t.Names.DeadLetterTopic(name, consumer.GetName()))
			if err := deleteIgnoringMissing(ctx, "topic "+deadLetters, func() error {
				_, err := service.Projects.Topics.Delete(deadLetters).Context(ctx).Do()
				return err
			}); err != nil {
				return err
			}
		}
		if t.Topics[name].GetCron() != "" {
			if err := t.removeSchedule(ctx, clients, name); err != nil {
				return err
			}
		}
		topic := topicPath(clients.Project, t.Names.Topic(name))
		if err := deleteIgnoringMissing(ctx, "topic "+topic, func() error {
			_, err := service.Projects.Topics.Delete(topic).Context(ctx).Do()
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func deleteIgnoringMissing(ctx context.Context, what string, call func() error) error {
	if err := retried(ctx, call); err != nil && !isAnswered(err, http.StatusNotFound) {
		return fmt.Errorf("delete Pub/Sub %s: %w", what, err)
	}
	return nil
}
