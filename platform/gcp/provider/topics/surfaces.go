package topics

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	minRetention = 10 * time.Minute
	maxRetention = 31 * 24 * time.Hour

	DefaultSubscriptionRetention = "604800s"
)

type TopicUpdate struct {
	Topic pubsub.Topic
	Mask  []string
}

func (u *TopicUpdate) send(field string) { u.Mask = append(u.Mask, field) }

var TopicSurface = surface.Surface[pubsub.Topic, TopicUpdate]{
	Name: "topic",
	Fields: []surface.Field[pubsub.Topic, TopicUpdate]{
		{
			Name: "message retention",
			Paths: []surface.Path[pubsub.Topic]{surface.NewPath("messageRetentionDuration", func(desired *pubsub.Topic, retention string) error {
				if _, err := parseRetention(retention); err != nil {
					return err
				}
				desired.MessageRetentionDuration = retention
				return nil
			})},
			Same: func(current, desired *pubsub.Topic) bool {
				return isSameDuration(current.MessageRetentionDuration, desired.MessageRetentionDuration)
			},
			Update: func(update *TopicUpdate, _, desired *pubsub.Topic) {
				update.Topic.MessageRetentionDuration = desired.MessageRetentionDuration
				update.send("messageRetentionDuration")
			},
			Show: func(topic *pubsub.Topic) string { return orOff(topic.MessageRetentionDuration) },
		},
		{
			Name: "encryption key",
			Paths: []surface.Path[pubsub.Topic]{surface.NewPath("kmsKeyName", func(desired *pubsub.Topic, key string) error {
				desired.KmsKeyName = key
				return nil
			})},
			Same: func(current, desired *pubsub.Topic) bool { return current.KmsKeyName == desired.KmsKeyName },
			Update: func(update *TopicUpdate, _, desired *pubsub.Topic) {
				update.Topic.KmsKeyName = desired.KmsKeyName
				update.send("kmsKeyName")
			},
			Show: func(topic *pubsub.Topic) string {
				if topic.KmsKeyName == "" {
					return "Google-managed"
				}
				return topic.KmsKeyName
			},
		},
		{
			Name: "message storage policy",
			Paths: []surface.Path[pubsub.Topic]{
				surface.NewPath("messageStoragePolicy.allowedPersistenceRegions", func(desired *pubsub.Topic, regions []string) error {
					desired.MessageStoragePolicy = withStoragePolicy(desired.MessageStoragePolicy, func(policy *pubsub.MessageStoragePolicy) {
						policy.AllowedPersistenceRegions = regions
					})
					return nil
				}),
				surface.NewPath("messageStoragePolicy.enforceInTransit", func(desired *pubsub.Topic, enforced bool) error {
					desired.MessageStoragePolicy = withStoragePolicy(desired.MessageStoragePolicy, func(policy *pubsub.MessageStoragePolicy) {
						policy.EnforceInTransit = enforced
					})
					return nil
				}),
			},
			Same: func(current, desired *pubsub.Topic) bool {
				held, wanted := withStoragePolicy(current.MessageStoragePolicy, func(*pubsub.MessageStoragePolicy) {}), withStoragePolicy(desired.MessageStoragePolicy, func(*pubsub.MessageStoragePolicy) {})
				return held.EnforceInTransit == wanted.EnforceInTransit &&
					slices.Equal(slices.Sorted(slices.Values(held.AllowedPersistenceRegions)), slices.Sorted(slices.Values(wanted.AllowedPersistenceRegions)))
			},
			Update: func(update *TopicUpdate, _, desired *pubsub.Topic) {
				update.Topic.MessageStoragePolicy = desired.MessageStoragePolicy
				update.send("messageStoragePolicy")
			},
			Show: func(topic *pubsub.Topic) string {
				if topic.MessageStoragePolicy == nil || len(topic.MessageStoragePolicy.AllowedPersistenceRegions) == 0 {
					return "any region"
				}
				return strings.Join(topic.MessageStoragePolicy.AllowedPersistenceRegions, ", ")
			},
		},
	},
	Labels: &surface.Labels[pubsub.Topic, TopicUpdate]{
		Of: func(topic *pubsub.Topic) *map[string]string { return &topic.Labels },
		Update: func(update *TopicUpdate, set map[string]string, removed []string) {
			update.Topic.Labels = changedLabels(update.Topic.Labels, set, removed)
			update.send("labels")
		},
	},
}

type SubscriptionUpdate struct {
	Subscription pubsub.Subscription
	Mask         []string
}

func (u *SubscriptionUpdate) send(field string) { u.Mask = append(u.Mask, field) }

var SubscriptionSurface = surface.Surface[pubsub.Subscription, SubscriptionUpdate]{
	Name: "subscription",
	Fields: []surface.Field[pubsub.Subscription, SubscriptionUpdate]{
		{
			Name: "acknowledged message retention",
			Paths: []surface.Path[pubsub.Subscription]{surface.NewPath("retainAckedMessages", func(desired *pubsub.Subscription, retained bool) error {
				desired.RetainAckedMessages = retained
				return nil
			})},
			Same: func(current, desired *pubsub.Subscription) bool {
				return current.RetainAckedMessages == desired.RetainAckedMessages
			},
			Update: func(update *SubscriptionUpdate, _, desired *pubsub.Subscription) {
				update.Subscription.RetainAckedMessages = desired.RetainAckedMessages
				update.send("retainAckedMessages")
			},
			Show: func(subscription *pubsub.Subscription) string {
				return surface.ShowOnOff(subscription.RetainAckedMessages)
			},
		},
		{
			Name: "message retention",
			Paths: []surface.Path[pubsub.Subscription]{surface.NewPath("messageRetentionDuration", func(desired *pubsub.Subscription, retention string) error {
				if _, err := parseRetention(retention); err != nil {
					return err
				}
				desired.MessageRetentionDuration = retention
				return nil
			})},
			Reset: func(desired *pubsub.Subscription) {
				if desired.MessageRetentionDuration == "" {
					desired.MessageRetentionDuration = DefaultSubscriptionRetention
				}
			},
			Same: func(current, desired *pubsub.Subscription) bool {
				return isSameDuration(current.MessageRetentionDuration, desired.MessageRetentionDuration)
			},
			Update: func(update *SubscriptionUpdate, _, desired *pubsub.Subscription) {
				update.Subscription.MessageRetentionDuration = desired.MessageRetentionDuration
				update.send("messageRetentionDuration")
			},
			Show: func(subscription *pubsub.Subscription) string { return orOff(subscription.MessageRetentionDuration) },
		},
	},
	Labels: &surface.Labels[pubsub.Subscription, SubscriptionUpdate]{
		Of: func(subscription *pubsub.Subscription) *map[string]string { return &subscription.Labels },
		Update: func(update *SubscriptionUpdate, set map[string]string, removed []string) {
			update.Subscription.Labels = changedLabels(update.Subscription.Labels, set, removed)
			update.send("labels")
		},
	},
}

func parseRetention(written string) (time.Duration, error) {
	retention, err := parseSeconds(written)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number of seconds ending in s, such as 86400s", written)
	}
	if retention < minRetention || retention > maxRetention {
		return 0, fmt.Errorf("%s is outside the 10 minutes to 31 days Pub/Sub retains a message for", written)
	}
	return retention, nil
}

func isSameDuration(current, desired string) bool {
	if current == desired {
		return true
	}
	held, heldErr := time.ParseDuration(current)
	wanted, wantedErr := time.ParseDuration(desired)
	return heldErr == nil && wantedErr == nil && held == wanted
}

func orOff(duration string) string {
	if duration == "" {
		return "off"
	}
	return duration
}

func withStoragePolicy(current *pubsub.MessageStoragePolicy, change func(*pubsub.MessageStoragePolicy)) *pubsub.MessageStoragePolicy {
	policy := pubsub.MessageStoragePolicy{}
	if current != nil {
		policy = *current
	}
	change(&policy)
	return &policy
}

func changedLabels(current, set map[string]string, removed []string) map[string]string {
	labels := maps.Clone(current)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, set)
	for _, key := range removed {
		delete(labels, key)
	}
	return labels
}
