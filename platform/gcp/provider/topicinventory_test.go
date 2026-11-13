package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func topicInventory(t *testing.T, pass transform.Pass) *costv1.ResourceSet {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	set, err := p.Inventory(context.Background(), provider.InventoryRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"},
		Resources: topicStack().Resources,
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	return set
}

func listedOfType(t *testing.T, set *costv1.ResourceSet, kind string) *costv1.Resource {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() == kind {
			return resource
		}
	}
	t.Fatalf("Inventory() lists %v, want a %s", set.GetResources(), kind)
	return nil
}

func TestATopicThatRetainsMessagesListsItsRetention(t *testing.T) {
	set := topicInventory(t, &patching{patches: transform.Patches{"topic": {"messageRetentionDuration": "86400s"}}})

	if retention := listedOfType(t, set, gcloud.PubSubTopicType).GetProperties().AsMap()["message_retention_duration"]; retention != "86400s" {
		t.Errorf("the topic lists retention %v, want the day its patch sets", retention)
	}
}

func TestASubscriptionThatRetainsAcknowledgedMessagesListsIt(t *testing.T) {
	set := topicInventory(t, &patching{patches: transform.Patches{"subscription": {"retainAckedMessages": true}}})

	if retained := listedOfType(t, set, gcloud.PubSubSubscriptionType).GetProperties().AsMap()["retain_acked_messages"]; retained != true {
		t.Errorf("the subscription lists retain_acked_messages %v, want true", retained)
	}
	if retention, listed := listedOfType(t, set, gcloud.PubSubTopicType).GetProperties().AsMap()["message_retention_duration"]; listed {
		t.Errorf("the topic lists retention %v, want none: it retains nothing", retention)
	}
}

func TestATopicThatRetainsNothingListsNoRetention(t *testing.T) {
	set := topicInventory(t, &patching{})

	if retention, listed := listedOfType(t, set, gcloud.PubSubTopicType).GetProperties().AsMap()["message_retention_duration"]; listed {
		t.Errorf("the topic lists retention %v, want none", retention)
	}
	if retained := listedOfType(t, set, gcloud.PubSubSubscriptionType).GetProperties().AsMap()["retain_acked_messages"]; retained != false {
		t.Errorf("the subscription lists retain_acked_messages %v, want false", retained)
	}
}

func TestATopicWhoseRetentionABindingFillsListsItAsUnknown(t *testing.T) {
	set := topicInventory(t, &patching{patches: transform.Patches{"topic": {
		"messageRetentionDuration": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "queues", "property": "retention"}},
	}}})

	if unknown := listedOfType(t, set, gcloud.PubSubTopicType).GetUnknown(); !slices.Contains(unknown, "message_retention_duration") {
		t.Errorf("the topic lists unknown %v, want its retention unknown until the deploy reads the record", unknown)
	}
}
