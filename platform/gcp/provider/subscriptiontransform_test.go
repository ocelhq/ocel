package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func twoConsumerStack() provider.StackSpec {
	spec := topicStack()
	spec.Resources[0].Topic = consumersNamed(false, "ship", "bill")
	return spec
}

func subscriptionPath(t *testing.T, p *Provider, consumer string) string {
	t.Helper()
	return "projects/acme-prod/subscriptions/" + topicNamed(t, topic.Names(namesOf(t, p), topicStack().Ref), "orders") + "-" + consumer
}

func sentToSubscriptions(server *topicsServer) []sentPatch {
	var sent []sentPatch
	for _, each := range server.sent() {
		if strings.Contains(each.name, "/subscriptions/") {
			sent = append(sent, each)
		}
	}
	return sent
}

func TestAPatchToATopicsSubscriptionsIsPlannedAndSentToEachOneThatExists(t *testing.T) {
	pass := &patching{}
	p, server := transformingTopics(t, pass)
	provisionTopicStack(t, p, twoConsumerStack())
	bill := subscriptionPath(t, p, "bill")
	server.holdSubscription(bill)

	pass.patches = transform.Patches{"subscription": {"retainAckedMessages": true, "messageRetentionDuration": "86400s"}}
	pass.tags = map[string]string{"team": "orders"}
	change := planTopicStack(t, p, twoConsumerStack())
	for _, want := range []string{
		"consumer bill's subscription's acknowledged message retention changes from off to on",
		"consumer bill's subscription's message retention changes from 604800s to 86400s",
		"consumer bill's subscription's labels team are set",
	} {
		if !strings.Contains(change.Reason, want) {
			t.Errorf("Plan() reason = %q, want it to say %q", change.Reason, want)
		}
	}
	if strings.Contains(change.Reason, "consumer ship") {
		t.Errorf("Plan() reason = %q, want nothing said of ship, whose worker has not made its subscription", change.Reason)
	}

	provisionTopicStack(t, p, twoConsumerStack())
	sent := sentToSubscriptions(server)
	if len(sent) != 1 || sent[0].name != bill || !slices.Equal(sent[0].mask, []string{"retainAckedMessages", "messageRetentionDuration", "labels"}) {
		t.Fatalf("patched subscriptions %+v, want bill's alone under the mask of what changed", sent)
	}
	if held := server.subscription(bill); held["retainAckedMessages"] != true || held["messageRetentionDuration"] != "86400s" {
		t.Errorf("bill's subscription holds %v, want acknowledged messages kept a day", held)
	}
}

func TestRemovingASubscriptionPatchPutsBackGooglesRetention(t *testing.T) {
	pass := &patching{patches: transform.Patches{"subscription": {"retainAckedMessages": true, "messageRetentionDuration": "86400s"}}}
	p, server := transformingTopics(t, pass)
	ship := subscriptionPath(t, p, "ship")
	server.holdSubscription(ship)
	provisionTopicStack(t, p, topicStack())

	pass.patches = nil
	provisionTopicStack(t, p, topicStack())

	if held := server.subscription(ship); held["retainAckedMessages"] != nil || held["messageRetentionDuration"] != "604800s" {
		t.Errorf("ship's subscription holds %v, want acknowledged messages dropped and Google's seven days back", held)
	}
	before := len(sentToSubscriptions(server))
	provisionTopicStack(t, p, topicStack())
	if after := len(sentToSubscriptions(server)); after != before {
		t.Errorf("patched %d more times, want nothing sent once the subscription is back as ocel sets it", after-before)
	}
}
