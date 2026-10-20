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
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	pushAckDeadline     = 600 * time.Second
	maxBackoff          = 600 * time.Second
	mutableSubscription = "pushConfig,retryPolicy,ackDeadlineSeconds"

	PublisherRole = "roles/pubsub.publisher"
)

type Push struct {
	URL            string
	ServiceAccount string
}

type Topology struct {
	Names  Names
	Topics map[string]*provider.TopicSpec
}

type Subscriptions struct {
	Names  Names
	Topics map[string]*provider.TopicSpec
	Pushes map[string]Push
}

func PushPath(topic, consumer string) string {
	return pushPrefix + topic + consumerInfix + consumer
}

func TopicPath(project, topic string) string { return "projects/" + project + "/topics/" + topic }

func (t Topology) Ensure(ctx context.Context, clients *ports.Clients) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		topic := t.Topics[name]
		path, err := t.Names.topicPath(clients.Project, name)
		if err != nil {
			return err
		}
		if err := ensureTopic(ctx, service, path); err != nil {
			return err
		}
		if isScheduled(topic) {
			if err := t.ensureSchedule(ctx, clients, name, topic); err != nil {
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
			path, err := s.Names.subscriptionPath(clients.Project, name, consumer.Name)
			if err != nil {
				return err
			}
			desired, err := s.subscription(clients.Project, name, topic, consumer, push)
			if err != nil {
				return err
			}
			if err := ensureSubscription(ctx, service, path, desired); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Subscriptions) subscription(project, name string, topic *provider.TopicSpec, consumer provider.ConsumerSpec, push Push) (*pubsub.Subscription, error) {
	path, err := s.Names.topicPath(project, name)
	if err != nil {
		return nil, err
	}
	return &pubsub.Subscription{
		Topic:                 path,
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
		ExpirationPolicy: &pubsub.ExpirationPolicy{},
	}, nil
}

func durationText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

func ensureTopic(ctx context.Context, service *pubsub.Service, path string) error {
	err := googleretry.Done(ctx, func() error {
		_, err := service.Projects.Topics.Create(path, &pubsub.Topic{}).Context(ctx).Do()
		return err
	})
	if err != nil && !isAnswered(err, http.StatusConflict) {
		return fmt.Errorf("create Pub/Sub topic %s: %w", path, err)
	}
	return nil
}

func ensureSubscription(ctx context.Context, service *pubsub.Service, path string, desired *pubsub.Subscription) error {
	err := googleretry.Done(ctx, func() error {
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
	err = googleretry.Done(ctx, func() error {
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
	err = googleretry.Done(ctx, func() error {
		_, err := service.Projects.Subscriptions.Patch(path, &pubsub.UpdateSubscriptionRequest{Subscription: desired, UpdateMask: mutableSubscription}).Context(ctx).Do()
		return err
	})
	if err != nil {
		return fmt.Errorf("update Pub/Sub subscription %s: %w", path, err)
	}
	return nil
}

func TopicPolicy(service *pubsub.Service, path string) policy.Resource {
	return policy.Resource{Name: "the Pub/Sub topic " + path, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		held, err := googleretry.Attempted(ctx, service.Projects.Topics.GetIamPolicy(path).Context(ctx).Do)
		if isAnswered(err, http.StatusNotFound) {
			return nil, nil, policy.Absent(err)
		}
		if err != nil {
			return nil, nil, err
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*pubsub.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &pubsub.Binding{Role: binding.Role, Members: binding.Members, Condition: topicExpr(binding.Condition)})
			}
			if policy.IsConditioned(bindings) {
				held.Version = policy.ConditionalVersion
			}
			_, err := googleretry.Attempted(ctx, service.Projects.Topics.SetIamPolicy(path, &pubsub.SetIamPolicyRequest{Policy: held}).Context(ctx).Do)
			if isAnswered(err, http.StatusNotFound) {
				return policy.Absent(err)
			}
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: topicCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func topicCondition(condition *pubsub.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func topicExpr(condition *policy.Condition) *pubsub.Expr {
	if condition == nil {
		return nil
	}
	return &pubsub.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func (t Topology) removeConsumers(ctx context.Context, clients *ports.Clients, topic string, consumers []string) error {
	service, err := clients.PubSub()
	if err != nil {
		return err
	}
	for _, consumer := range consumers {
		subscription, err := t.Names.subscriptionPath(clients.Project, topic, consumer)
		if err != nil {
			return err
		}
		if err := deleteIgnoringMissing(ctx, "subscription "+subscription, func() error {
			_, err := service.Projects.Subscriptions.Delete(subscription).Context(ctx).Do()
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func findDroppedConsumers(before, after *provider.TopicSpec) []string {
	var dropped []string
	for _, consumer := range before.Consumers {
		if !slices.ContainsFunc(after.Consumers, func(kept provider.ConsumerSpec) bool { return kept.Name == consumer.Name }) {
			dropped = append(dropped, consumer.Name)
		}
	}
	return dropped
}

func isCronDropped(before, after *provider.TopicSpec) bool {
	return before.Cron != "" && after.Cron == ""
}

func isScheduled(topic *provider.TopicSpec) bool { return topic.Cron != "" && runs.IsTask(topic) }

func (t Topology) RemoveDropped(ctx context.Context, clients *ports.Clients, previous map[string]*provider.TopicSpec) error {
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		before, declared := previous[name]
		if !declared {
			continue
		}
		if err := t.removeConsumers(ctx, clients, name, findDroppedConsumers(before, t.Topics[name])); err != nil {
			return err
		}
		if isCronDropped(before, t.Topics[name]) {
			if err := t.removeSchedule(ctx, clients, name, before); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t Topology) FindMissing(ctx context.Context, clients *ports.Clients) ([]string, error) {
	service, err := clients.PubSub()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		path, err := t.Names.topicPath(clients.Project, name)
		if err != nil {
			return nil, err
		}
		_, err = googleretry.Attempted(ctx, service.Projects.Topics.Get(path).Context(ctx).Do)
		switch {
		case isAnswered(err, http.StatusNotFound):
			missing = append(missing, name)
		case err != nil:
			return nil, fmt.Errorf("read Pub/Sub topic %s: %w", path, err)
		}
	}
	return missing, nil
}

func (t Topology) ReadDifferences(ctx context.Context, clients *ports.Clients, previous map[string]*provider.TopicSpec) ([]string, error) {
	service, err := clients.PubSub()
	if err != nil {
		return nil, err
	}
	var differences []string
	for _, name := range slices.Sorted(maps.Keys(t.Topics)) {
		topic := t.Topics[name]
		if before, declared := previous[name]; declared {
			for _, consumer := range findDroppedConsumers(before, topic) {
				path, err := t.Names.subscriptionPath(clients.Project, name, consumer)
				if err != nil {
					return nil, err
				}
				_, err = googleretry.Attempted(ctx, service.Projects.Subscriptions.Get(path).Context(ctx).Do)
				switch {
				case isAnswered(err, http.StatusNotFound):
				case err != nil:
					return nil, fmt.Errorf("read Pub/Sub subscription %s: %w", path, err)
				default:
					differences = append(differences, "consumer "+consumer+" of "+name+" is no longer declared, so its Pub/Sub subscription "+path+" is deleted")
				}
			}
			if isCronDropped(before, topic) {
				difference, err := t.readDroppedScheduleDifference(ctx, clients, name, before)
				if err != nil {
					return nil, err
				}
				differences = appendDifference(differences, difference)
			}
		}
		if isScheduled(topic) {
			difference, err := t.readScheduleDifference(ctx, clients, name, topic)
			if err != nil {
				return nil, err
			}
			differences = appendDifference(differences, difference)
		}
	}
	return differences, nil
}

func appendDifference(differences []string, difference string) []string {
	if difference == "" {
		return differences
	}
	return append(differences, difference)
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
			if err := t.removeSchedule(ctx, clients, name, t.Topics[name]); err != nil {
				return err
			}
		}
		topic, err := t.Names.topicPath(clients.Project, name)
		if err != nil {
			return err
		}
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
	if err := googleretry.Done(ctx, call); err != nil && !isAnswered(err, http.StatusNotFound) {
		return fmt.Errorf("delete Pub/Sub %s: %w", what, err)
	}
	return nil
}

func isAnswered(err error, code int) bool { return googleretry.AnsweredCode(err) == code }
