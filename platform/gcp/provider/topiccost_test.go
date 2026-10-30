package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

func pricedTopic(t *testing.T, pass transform.Pass) (*costv1.ResourceSet, *costv1.Estimate) {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	set, err := p.InventoryDeploy(context.Background(), provider.InventoryRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"},
		Resources: topicStack().Resources,
	})
	if err != nil {
		t.Fatalf("InventoryDeploy() = %v", err)
	}
	estimate, err := cost.Price(&costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_MODERATE}})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	return set, estimate
}

func inventoriedOfType(t *testing.T, set *costv1.ResourceSet, kind string) *costv1.Resource {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() == kind {
			return resource
		}
	}
	t.Fatalf("InventoryDeploy() inventoried %v, want a %s", set.GetResources(), kind)
	return nil
}

func storageOf(estimate *costv1.Estimate, resource *costv1.Resource) *costv1.CostComponent {
	for _, priced := range estimate.GetResources() {
		if priced.GetResource() != resource.GetId() {
			continue
		}
		for _, component := range priced.GetComponents() {
			if component.GetName() == "Message storage" {
				return component
			}
		}
	}
	return nil
}

func TestATopicThatRetainsMessagesIsPricedForTheirStorage(t *testing.T) {
	set, estimate := pricedTopic(t, &patching{patches: transform.Patches{"topic": {"messageRetentionDuration": "86400s"}}})

	storage := storageOf(estimate, inventoriedOfType(t, set, cost.PubSubTopicType))
	if storage == nil || storage.GetRateRef() != "gcp/pubsub/storage" || storage.GetMonthlyCost() == "0.00" {
		t.Errorf("the topic's message storage is priced %v, want a day of its published messages at Pub/Sub's storage rate", storage)
	}
}

func TestASubscriptionThatRetainsAcknowledgedMessagesIsPricedForTheirStorage(t *testing.T) {
	set, estimate := pricedTopic(t, &patching{patches: transform.Patches{"subscription": {"retainAckedMessages": true}}})

	if storage := storageOf(estimate, inventoriedOfType(t, set, cost.PubSubSubscriptionType)); storage == nil || storage.GetMonthlyCost() == "0.00" {
		t.Errorf("the subscription's message storage is priced %v, want seven days of its delivered messages", storage)
	}
	if storage := storageOf(estimate, inventoriedOfType(t, set, cost.PubSubTopicType)); storage != nil {
		t.Errorf("the topic is priced for message storage %v, want none: it retains nothing", storage)
	}
}

func TestATopicThatRetainsNothingIsPricedForNoStorage(t *testing.T) {
	set, estimate := pricedTopic(t, &patching{})

	for _, kind := range []string{cost.PubSubTopicType, cost.PubSubSubscriptionType} {
		if storage := storageOf(estimate, inventoriedOfType(t, set, kind)); storage != nil {
			t.Errorf("the %s is priced for message storage %v, want none", kind, storage)
		}
	}
}

func TestATopicWhoseRetentionABindingFillsIsPricedAsUnknown(t *testing.T) {
	set, _ := pricedTopic(t, &patching{patches: transform.Patches{"topic": {
		"messageRetentionDuration": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "queues", "property": "retention"}},
	}}})

	if unknown := inventoriedOfType(t, set, cost.PubSubTopicType).GetUnknown(); !slices.Contains(unknown, "message_retention_duration") {
		t.Errorf("the topic is priced with unknown %v, want its retention unknown until the deploy reads the record", unknown)
	}
}
