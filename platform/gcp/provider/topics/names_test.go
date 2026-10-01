package topics_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

var pubsubName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9\-_.~+%]{2,254}$`)

func TestEveryPubSubNameIsOnePubSubTakesAndNamesOneDeclarationOnly(t *testing.T) {
	names := topics.Names{Namespace: "ocel", Scope: topics.Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "production"}}

	seen := map[string]string{}
	for _, declared := range []struct{ topic, consumer string }{
		{"orders-eu", "ship"},
		{"orders", "eu-ship"},
		{"orders", "ship"},
		{"resize-image", "resize-image"},
	} {
		for kind, name := range map[string]string{
			"topic":             names.Topic(declared.topic),
			"subscription":      names.Subscription(declared.topic, declared.consumer),
			"dead-letter topic": names.DeadLetterTopic(declared.topic, declared.consumer),
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
