package gcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const liveSlug = "shop"

func infraRef(t *testing.T) provider.StackRef {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return provider.StackRef{Project: liveSlug, Tier: environment.TierPreview, Name: naming.InfraStack("t" + hex.EncodeToString(sum[:5]))}
}

func taskNamesOf(t *testing.T, ref provider.StackRef) topics.Names {
	t.Helper()
	return topics.Names{
		Namespace: liveNames(t).Namespace(),
		Scope:     topics.Scope{Slug: ref.Project, Tier: ref.Tier, Environment: ref.Name.Env},
	}
}

func topicExists(t *testing.T, name string) bool {
	t.Helper()
	service, err := workloadClients(t).PubSub()
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Projects.Topics.Get("projects/" + liveProject() + "/topics/" + name).Context(context.Background()).Do()
	return err == nil
}

func ordersSpec(consumers ...string) *provider.TopicSpec {
	spec := &provider.TopicSpec{}
	for _, consumer := range consumers {
		spec.Consumers = append(spec.Consumers, provider.ConsumerSpec{
			Name: consumer, Worker: "worker",
			Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute},
		})
	}
	return spec
}

func TestLiveATopicIsProvisionedAsItsPubSubTopologyAndRemovedWithItsBinding(t *testing.T) {
	p := live(t)
	bootstrapped(t, p, environment.TierPreview, gcp.TasksFeature)
	ctx := context.Background()
	ref := infraRef(t)
	names := taskNamesOf(t, ref)
	orders := provider.Resource{Name: "topic-orders", Declared: "orders", Type: provider.BindingTopic, Topic: ordersSpec("ship", "bill")}

	binding, err := p.ProvisionTopic(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	t.Cleanup(func() { _ = p.RemoveResource(ctx, ref, binding, nil) })
	if binding.Type != provider.BindingTopic || binding.Name != "topic-orders" || binding.Resource != "orders" {
		t.Errorf("ProvisionTopic() = %+v, want the topic binding named for its resource, of the topic an app reaches by its declared name", binding)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Errorf("VerifyProperties(%+v) = %v", binding, err)
	}
	for _, topic := range []string{names.Topic("orders"), names.DeadLetterTopic("orders", "ship"), names.DeadLetterTopic("orders", "bill")} {
		if !topicExists(t, topic) {
			t.Errorf("the Pub/Sub topic %s is missing after ProvisionTopic()", topic)
		}
	}

	if err := stackrecords.Write(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra, Bindings: []provider.Binding{binding},
	}); err != nil {
		t.Fatal(err)
	}
	orders.Topic = ordersSpec("ship")
	binding, err = p.ProvisionTopic(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() without bill = %v", err)
	}
	if topicExists(t, names.DeadLetterTopic("orders", "bill")) {
		t.Error("the dead-letter topic of orders/bill outlived the deploy that dropped bill")
	}
	if !topicExists(t, names.DeadLetterTopic("orders", "ship")) {
		t.Error("the dead-letter topic of orders/ship went with bill's")
	}

	if err := p.RemoveResource(ctx, ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	for _, topic := range []string{names.Topic("orders"), names.DeadLetterTopic("orders", "ship")} {
		if topicExists(t, topic) {
			t.Errorf("the Pub/Sub topic %s outlived its binding", topic)
		}
	}
}

func TestLiveATopicWhoseOrderingChangedIsRefusedBeforeAnythingChanges(t *testing.T) {
	p := live(t)
	bootstrapped(t, p, environment.TierPreview, gcp.TasksFeature)
	ctx := context.Background()
	ref := infraRef(t)
	names := taskNamesOf(t, ref)
	ordered := ordersSpec("ship", "bill")
	ordered.Ordered = true
	orders := provider.Resource{Name: "topic-orders", Declared: "orders", Type: provider.BindingTopic, Topic: ordered}

	binding, err := p.ProvisionTopic(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	t.Cleanup(func() { _ = p.RemoveResource(ctx, ref, binding, nil) })
	if err := stackrecords.Write(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra, Bindings: []provider.Binding{binding},
	}); err != nil {
		t.Fatal(err)
	}

	orders.Topic = ordersSpec("ship")
	_, err = p.ProvisionTopic(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err == nil || !strings.Contains(err.Error(), "ship") {
		t.Fatalf("ProvisionTopic() after orders stopped being ordered = %v, want it refused naming the consumer it keeps", err)
	}
	if !topicExists(t, names.DeadLetterTopic("orders", "bill")) {
		t.Error("the refused deploy removed bill's dead-letter topic, want nothing changed before the refusal")
	}
}

func TestLiveATopicDeclaredWithNoConfigIsAPubSubTopicAlone(t *testing.T) {
	p := live(t)
	bootstrapped(t, p, environment.TierPreview, gcp.TasksFeature)
	ctx := context.Background()
	ref := infraRef(t)

	binding, err := p.ProvisionTopic(ctx, resources.ProvisionRequest{Ref: ref, Resource: provider.Resource{Name: "c-topic", Type: provider.BindingTopic}}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() of a topic with no config = %v", err)
	}
	t.Cleanup(func() { _ = p.RemoveResource(ctx, ref, binding, nil) })
	if !topicExists(t, taskNamesOf(t, ref).Topic("c-topic")) {
		t.Error("a topic declared with no config made no Pub/Sub topic")
	}
}
