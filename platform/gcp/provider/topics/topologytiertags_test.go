package topics_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func taggedPubSub(t *testing.T) (*googletest.PubSub, *ports.Clients, topics.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	fake := googletest.NewPubSub()
	fake.TierTags = tags
	server := httptest.NewServer(fake.Serve(t))
	t.Cleanup(server.Close)
	clients := tags.Tagged(googletest.Clients(server.URL))
	return fake, clients.Workload(), topic.TierTagsOf(clients, environment.TierProduction)
}

func TestEveryTopicAndSubscriptionOfATierCarriesItsTiersTagFromItsCreation(t *testing.T) {
	ctx := context.Background()
	fake, workload, tags := taggedPubSub(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}

	if err := (topics.Topology{Names: names, Topics: ordersAndResize(), Tags: tags}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := (topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app"), Tags: tags}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	made := slices.Concat(fake.Created, []string{topics.TopicPath("acme-prod", topicOf(t, names, "orders")), topics.TopicPath("acme-prod", topicOf(t, names, "resize"))})
	for _, path := range made {
		if got := fake.InlineTags[path]; got["acme-prod/ocel-tier"] != "production" {
			t.Errorf("%s was created with tags %v, want acme-prod/ocel-tier: production", path, got)
		}
	}
	if fake.TierTags.BindingsAsked != 0 {
		t.Errorf("Resource Manager was asked for %d bindings, want none: each topic and subscription carried its tag from its creation", fake.TierTags.BindingsAsked)
	}
}

func TestATopicAndSubscriptionFoundAlreadyMadeAreBoundToTheirTiersValue(t *testing.T) {
	ctx := context.Background()
	fake, workload, tags := taggedPubSub(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	untaggedAtCreation := tags
	untaggedAtCreation.Inline = nil
	if err := (topics.Topology{Names: names, Topics: ordersAndResize(), Tags: untaggedAtCreation}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := (topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app"), Tags: untaggedAtCreation}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}

	if err := (topics.Topology{Names: names, Topics: ordersAndResize(), Tags: tags}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := (topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app"), Tags: tags}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	found := slices.Concat(fake.Created, []string{topics.TopicPath("acme-prod", topicOf(t, names, "orders")), topics.TopicPath("acme-prod", topicOf(t, names, "resize"))})
	for _, path := range found {
		if got := fake.TierTags.Bound(googletest.PubSubTagParent(path)); !slices.Equal(got, []string{fake.TierTags.ValueOf(environment.TierProduction)}) {
			t.Errorf("%s, found already made, carries %v, want the production value", path, got)
		}
	}
}

func TestRemovingATopologyRemovesEachInlineTagBeforeItsTopicOrSubscription(t *testing.T) {
	ctx := context.Background()
	fake, workload, tags := taggedPubSub(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	topology := topics.Topology{Names: names, Topics: ordersAndResize(), Tags: tags}
	if err := topology.Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := (topics.Subscriptions{Names: names, Topics: ordersAndResize(), Pushes: pushesTo("https://worker.run.app"), Tags: tags}).Ensure(ctx, workload); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := topology.Remove(ctx, workload); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.Deleted) != 5 {
		t.Errorf("removing the topology deleted %v, want both topics and their three subscriptions", fake.Deleted)
	}
	if len(fake.DeletedTagged) != 0 {
		t.Errorf("%v were deleted still carrying their tier tag: Google keeps an inline binding of a deleted topic, and the tag value can never be deleted after", fake.DeletedTagged)
	}
}
