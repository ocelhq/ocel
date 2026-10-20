package topics_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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
			"topic":        names.Topic(declared.topic),
			"subscription": names.Subscription(declared.topic, declared.consumer),
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
	if other.Topic("orders") == names.Topic("orders") {
		t.Errorf("two environments share the topic %q, so a preview's sends reach production", names.Topic("orders"))
	}
}

func TestATopicIsNamedUnderTheAppFenceForItsProjectAndEnvironment(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "prod"}}

	if got, want := names.Topic("orders"), "ocel-app-shop-prod-orders-747a5d"; got != want {
		t.Errorf("Topic(orders) = %q, want %q", got, want)
	}
	if got, want := names.Subscription("orders", "ship"), names.Topic("orders")+"-ship"; got != want {
		t.Errorf("Subscription(orders, ship) = %q, want the topic and its consumer, %q", got, want)
	}
	if got, want := names.ScheduleJob("orders"), names.Topic("orders"); got != want {
		t.Errorf("ScheduleJob(orders) = %q, want the topic it publishes to, %q", got, want)
	}
}

func TestATopicWhoseSubscriptionCannotFitPubSubIsRefused(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "prod"}}
	declared := map[string]*provider.TopicSpec{
		"orders": {Consumers: []provider.ConsumerSpec{{Name: strings.Repeat("ship", 60)}}},
	}

	var refused refusal.Refusal
	if err := names.RefuseUnfit(declared); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("RefuseUnfit() = %v, want an %s refusal: a subscription is the topic and its consumer, and Pub/Sub takes 255 characters", err, refusal.CodeInvalid)
	}
	if err := names.RefuseUnfit(map[string]*provider.TopicSpec{"orders": {Consumers: []provider.ConsumerSpec{{Name: "ship"}}}}); err != nil {
		t.Errorf("RefuseUnfit() = %v, want a topic and consumer of ordinary length taken", err)
	}
}
