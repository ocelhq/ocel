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

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	pushAckDeadline         = 600 * time.Second
	maxBackoff              = 600 * time.Second
	deadLetterDeliveryLimit = 100
	mutableSubscription     = "pushConfig,retryPolicy,deadLetterPolicy,ackDeadlineSeconds"

	publisherRole  = "roles/pubsub.publisher"
	subscriberRole = "roles/pubsub.subscriber"
	grantAttempts  = 4
)

type Push struct {
	URL            string
	ServiceAccount string
}

type Topology struct {
	Names     Names
	Topics    map[string]*provider.TopicSpec
	Publisher string
	Agent     string
}

type Subscriptions struct {
	Names  Names
	Topics map[string]*provider.TopicSpec
	Pushes map[string]Push
	Agent  string
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
		path := topicPath(clients.Project, t.Names.Topic(name))
		if err := ensureTopic(ctx, service, path); err != nil {
			return err
		}
		if err := grantOnTopic(ctx, service, path, publisherRole, t.Publisher); err != nil {
			return err
		}
		for _, consumer := range topic.Consumers {
			deadLetters := topicPath(clients.Project, t.Names.DeadLetterTopic(name, consumer.Name))
			if err := ensureTopic(ctx, service, deadLetters); err != nil {
				return err
			}
			if err := grantOnTopic(ctx, service, deadLetters, publisherRole, t.Agent); err != nil {
				return err
			}
		}
		if topic.Cron != "" && runs.IsTask(topic) {
			if err := t.ensureSchedule(ctx, clients, name, topic.Cron); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Subscriptions) Ensure(ctx context.Context, clients *ports.Clients) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(s.Topics)) {
		topic := s.Topics[name]
		for _, consumer := range topic.Consumers {
			push, hosted := s.Pushes[consumer.Worker]
			if !hosted {
				continue
			}
			deadLetters := topicPath(clients.Project, s.Names.DeadLetterTopic(name, consumer.Name))
			path := subscriptionPath(clients.Project, s.Names.Subscription(name, consumer.Name))
			if err := ensureSubscription(ctx, service, path, s.subscription(clients.Project, name, topic, consumer, push, deadLetters)); err != nil {
				return err
			}
			if err := grantOnSubscription(ctx, service, path, subscriberRole, s.Agent); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Subscriptions) subscription(project, name string, topic *provider.TopicSpec, consumer provider.ConsumerSpec, push Push, deadLetters string) *pubsub.Subscription {
	return &pubsub.Subscription{
		Topic:                 topicPath(project, s.Names.Topic(name)),
		AckDeadlineSeconds:    int64(pushAckDeadline / time.Second),
		EnableMessageOrdering: topic.Ordered,
		Filter:                fmt.Sprintf(`NOT attributes:%s OR attributes.%s = %q`, ConsumerAttribute, ConsumerAttribute, consumer.Name),
		PushConfig: &pubsub.PushConfig{
			PushEndpoint: push.URL + PushPath(name, consumer.Name),
			OidcToken:    &pubsub.OidcToken{ServiceAccountEmail: push.ServiceAccount, Audience: push.URL},
		},
		RetryPolicy: &pubsub.RetryPolicy{
			MinimumBackoff: durationText(min(consumer.Retry.MinDelay, maxBackoff)),
			MaximumBackoff: durationText(min(consumer.Retry.MaxDelay, maxBackoff)),
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

type iamPolicyCalls struct {
	read  func() (*pubsub.Policy, error)
	write func(*pubsub.Policy) error
}

func grantOnTopic(ctx context.Context, service *pubsub.Service, path, role, member string) error {
	return grantPubSubRole(ctx, path, role, member, iamPolicyCalls{
		read: func() (*pubsub.Policy, error) { return service.Projects.Topics.GetIamPolicy(path).Context(ctx).Do() },
		write: func(policy *pubsub.Policy) error {
			_, err := service.Projects.Topics.SetIamPolicy(path, &pubsub.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do()
			return err
		},
	})
}

func grantOnSubscription(ctx context.Context, service *pubsub.Service, path, role, member string) error {
	return grantPubSubRole(ctx, path, role, member, iamPolicyCalls{
		read: func() (*pubsub.Policy, error) {
			return service.Projects.Subscriptions.GetIamPolicy(path).Context(ctx).Do()
		},
		write: func(policy *pubsub.Policy) error {
			_, err := service.Projects.Subscriptions.SetIamPolicy(path, &pubsub.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do()
			return err
		},
	})
}

func grantPubSubRole(ctx context.Context, path, role, member string, calls iamPolicyCalls) error {
	if member == "" {
		return nil
	}
	var refused error
	for attempt := range grantAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		var policy *pubsub.Policy
		if err := retried(ctx, func() error {
			var readErr error
			policy, readErr = calls.read()
			return readErr
		}); err != nil {
			return fmt.Errorf("read who may reach %s: %w", path, err)
		}
		if holdsRole(policy, role, member) {
			return nil
		}
		policy.Bindings = withMember(policy.Bindings, role, member)
		refused = retried(ctx, func() error { return calls.write(policy) })
		if refused == nil || !isAnswered(refused, http.StatusConflict) {
			break
		}
	}
	if refused != nil {
		return fmt.Errorf("grant %s %s on %s: %w", member, role, path, refused)
	}
	return nil
}

func holdsRole(policy *pubsub.Policy, role, member string) bool {
	return slices.ContainsFunc(policy.Bindings, func(binding *pubsub.Binding) bool {
		return binding.Role == role && binding.Condition == nil && slices.Contains(binding.Members, member)
	})
}

func withMember(bindings []*pubsub.Binding, role, member string) []*pubsub.Binding {
	for _, binding := range bindings {
		if binding.Role == role && binding.Condition == nil {
			binding.Members = append(binding.Members, member)
			return bindings
		}
	}
	return append(bindings, &pubsub.Binding{Role: role, Members: []string{member}})
}

func (t Topology) removeConsumers(ctx context.Context, clients *ports.Clients, topic string, consumers []string) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, consumer := range consumers {
		subscription := subscriptionPath(clients.Project, t.Names.Subscription(topic, consumer))
		if err := deleteIgnoringMissing(ctx, "subscription "+subscription, func() error {
			_, err := service.Projects.Subscriptions.Delete(subscription).Context(ctx).Do()
			return err
		}); err != nil {
			return err
		}
		deadLetters := topicPath(clients.Project, t.Names.DeadLetterTopic(topic, consumer))
		if err := deleteIgnoringMissing(ctx, "topic "+deadLetters, func() error {
			_, err := service.Projects.Topics.Delete(deadLetters).Context(ctx).Do()
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func (t Topology) RemoveDropped(ctx context.Context, clients *ports.Clients, previous map[string]*provider.TopicSpec) error {
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		before, declared := previous[name]
		if !declared {
			continue
		}
		var dropped []string
		for _, consumer := range before.Consumers {
			if !slices.ContainsFunc(t.Topics[name].Consumers, func(kept provider.ConsumerSpec) bool { return kept.Name == consumer.Name }) {
				dropped = append(dropped, consumer.Name)
			}
		}
		if err := t.removeConsumers(ctx, clients, name, dropped); err != nil {
			return err
		}
		if before.Cron != "" && t.Topics[name].Cron == "" {
			if err := t.removeSchedule(ctx, clients, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t Topology) Remove(ctx context.Context, clients *ports.Clients) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		consumers := make([]string, 0, len(t.Topics[name].Consumers))
		for _, consumer := range t.Topics[name].Consumers {
			consumers = append(consumers, consumer.Name)
		}
		if err := t.removeConsumers(ctx, clients, name, consumers); err != nil {
			return err
		}
		if t.Topics[name].Cron != "" {
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
