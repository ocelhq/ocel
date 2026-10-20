package topics_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

var pubsubName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9\-_.~+%]{2,254}$`)

func TestEveryPubSubNameIsOnePubSubTakesAndNamesOneDeclarationOnly(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "prod"}}

	seen := map[string]string{}
	for _, declared := range []struct{ topic, consumer string }{
		{"orders-eu", "ship"},
		{"orders", "eu-ship"},
		{"orders", "ship"},
		{"resize-image", "resize-image"},
	} {
		for kind, name := range map[string]string{
			"topic":        topicOf(t, names, declared.topic),
			"subscription": subscriptionOf(t, names, declared.topic, declared.consumer),
		} {
			if !pubsubName.MatchString(name) || strings.HasPrefix(name, "goog") {
				t.Errorf("the %s of %s/%s is %q, which Pub/Sub refuses", kind, declared.topic, declared.consumer, name)
			}
			key := kind + " " + declared.topic + "/" + declared.consumer
			if kind == "topic" {
				key = kind + " " + declared.topic
			}
			if other, taken := seen[name]; taken && other != key {
				t.Errorf("%s and %s are both named %q", other, key, name)
			}
			seen[name] = key
		}
	}

	other := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: "pr-7"}}
	if topicOf(t, other, "orders") == topicOf(t, names, "orders") {
		t.Errorf("two environments share the topic %q, so a preview's sends reach production", topicOf(t, names, "orders"))
	}
}

func topicOf(t *testing.T, names topics.Names, topic string) string {
	t.Helper()
	name, err := names.Topic(topic)
	if err != nil {
		t.Fatalf("Topic(%s) = %v", topic, err)
	}
	return name
}

func subscriptionOf(t *testing.T, names topics.Names, topic, consumer string) string {
	t.Helper()
	name, err := names.Subscription(topic, consumer)
	if err != nil {
		t.Fatalf("Subscription(%s, %s) = %v", topic, consumer, err)
	}
	return name
}

func TestATopicIsNamedUnderTheAppFenceForItsProjectAndEnvironment(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "prod"}}

	if got, want := topicOf(t, names, "orders"), "ocel-app-shop-prod-orders-747a5d"; got != want {
		t.Errorf("Topic(orders) = %q, want %q", got, want)
	}
	if got, want := subscriptionOf(t, names, "orders", "ship"), "ocel-app-shop-prod-orders-747a5d-ship"; got != want {
		t.Errorf("Subscription(orders, ship) = %q, want the topic and its consumer, %q", got, want)
	}
}

func TestATopicLeavesRoomForTheLongestConsumerSoEverySubscriptionIsTheTopicAndItsConsumer(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: strings.Repeat("e", 200)}}
	consumer := strings.Repeat("c", 63)

	topic := topicOf(t, names, "orders")
	subscription := subscriptionOf(t, names, "orders", consumer)
	if subscription != topic+"-"+consumer || len(subscription) > 255 {
		t.Errorf("Subscription() = %q (%d characters), want %q and its consumer within the 255 Pub/Sub takes", subscription, len(subscription), topic)
	}
}

func TestAConsumerLongerThanASubscriptionLeavesItIsRefused(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "prod"}}

	var refused refusal.Refusal
	if _, err := names.Subscription("orders", strings.Repeat("c", 64)); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Subscription() = %v, want an %s refusal: a subscription is the topic and its consumer, and Pub/Sub takes 255 characters", err, refusal.CodeInvalid)
	}
	if _, err := names.Subscription("orders", "ship"); err != nil {
		t.Errorf("Subscription() = %v, want a topic and consumer of ordinary length taken", err)
	}
}

func TestASubscriptionFitsPubSubWhateverCharactersItsConsumerIsNamedWith(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: strings.Repeat("e", 200)}}
	consumer := "ship" + strings.Repeat("_", 30) + "x"

	subscription, err := names.Subscription("orders", consumer)
	if err == nil && len(subscription) > 255 {
		t.Errorf("Subscription() = %q, %d characters, want at most the 255 Pub/Sub takes or a refusal", subscription, len(subscription))
	}
}
