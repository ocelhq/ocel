//go:build integration

package gcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
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
	bootstrappedWith(t, p, environment.TierPreview, bootstrap.TopicsPart)
	writableDatabaseMade(t, p, environment.TierPreview, liveSlug)
	ctx := context.Background()
	ref := infraRef(t)
	names := taskNamesOf(t, ref)
	orders := provider.Resource{Name: "topic-orders", Declared: "orders", Type: provider.BindingTopic, Topic: ordersSpec("ship", "bill")}

	binding, err := p.TopicHooks().Provision(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	t.Cleanup(func() { _ = p.TopicHooks().Remove(ctx, ref, binding, nil) })
	if binding.Type != provider.BindingTopic || binding.Name != "topic-orders" || binding.Resource != "orders" {
		t.Errorf("ProvisionTopic() = %+v, want the topic binding named for its resource, of the topic an app reaches by its declared name", binding)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Errorf("VerifyProperties(%+v) = %v", binding, err)
	}
	if !topicExists(t, liveTopicNamed(t, names, "orders")) {
		t.Errorf("the Pub/Sub topic %s is missing after ProvisionTopic()", liveTopicNamed(t, names, "orders"))
	}

	pubsubService, err := workloadClients(t).PubSub()
	if err != nil {
		t.Fatal(err)
	}
	topicPolicy, err := pubsubService.Projects.Topics.GetIamPolicy("projects/" + liveProject() + "/topics/" + liveTopicNamed(t, names, "orders")).Context(ctx).Do()
	if err != nil {
		t.Fatalf("GetIamPolicy(%s) = %v", liveTopicNamed(t, names, "orders"), err)
	}
	tierAccount := "serviceAccount:ocel-" + string(ref.Tier) + "@" + liveProject() + ".iam.gserviceaccount.com"
	for _, bound := range topicPolicy.Bindings {
		if slices.Contains(bound.Members, tierAccount) {
			t.Errorf("topic %s grants %s to %s, and no account of the tier's own publishes: a delayed message is published as the app that sent it", liveTopicNamed(t, names, "orders"), bound.Role, tierAccount)
		}
	}

	if err := stackrecords.Write(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra, Bindings: []provider.Binding{binding},
	}); err != nil {
		t.Fatal(err)
	}
	orders.Topic = ordersSpec("ship")
	binding, err = p.TopicHooks().Provision(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() without bill = %v", err)
	}
	if !topicExists(t, liveTopicNamed(t, names, "orders")) {
		t.Errorf("the Pub/Sub topic %s went with its dropped consumer bill", liveTopicNamed(t, names, "orders"))
	}

	if err := p.TopicHooks().Remove(ctx, ref, binding, nil); err != nil {
		t.Fatalf("RemoveTopic() = %v", err)
	}
	if topicExists(t, liveTopicNamed(t, names, "orders")) {
		t.Errorf("the Pub/Sub topic %s outlived its binding", liveTopicNamed(t, names, "orders"))
	}
}

func TestLiveATopicWhoseOrderingChangedIsRefusedNamingTheConsumerItKeeps(t *testing.T) {
	p := live(t)
	bootstrappedWith(t, p, environment.TierPreview, bootstrap.TopicsPart)
	writableDatabaseMade(t, p, environment.TierPreview, liveSlug)
	ctx := context.Background()
	ref := infraRef(t)
	ordered := ordersSpec("ship", "bill")
	ordered.Ordered = true
	orders := provider.Resource{Name: "topic-orders", Declared: "orders", Type: provider.BindingTopic, Topic: ordered}

	binding, err := p.TopicHooks().Provision(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	t.Cleanup(func() { _ = p.TopicHooks().Remove(ctx, ref, binding, nil) })
	if err := stackrecords.Write(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra, Bindings: []provider.Binding{binding},
	}); err != nil {
		t.Fatal(err)
	}

	orders.Topic = ordersSpec("ship")
	_, err = p.TopicHooks().Provision(ctx, resources.ProvisionRequest{Ref: ref, Resource: orders}, nil)
	if err == nil || !strings.Contains(err.Error(), "ship") {
		t.Fatalf("ProvisionTopic() after orders stopped being ordered = %v, want it refused naming the consumer it keeps", err)
	}
}

func TestLiveATopicDeclaredWithNoConfigIsAPubSubTopicAlone(t *testing.T) {
	p := live(t)
	bootstrappedWith(t, p, environment.TierPreview, bootstrap.TopicsPart)
	writableDatabaseMade(t, p, environment.TierPreview, liveSlug)
	ctx := context.Background()
	ref := infraRef(t)

	binding, err := p.TopicHooks().Provision(ctx, resources.ProvisionRequest{Ref: ref, Resource: provider.Resource{Name: "c-topic", Type: provider.BindingTopic}}, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() of a topic with no config = %v", err)
	}
	t.Cleanup(func() { _ = p.TopicHooks().Remove(ctx, ref, binding, nil) })
	if !topicExists(t, liveTopicNamed(t, taskNamesOf(t, ref), "c-topic")) {
		t.Error("a topic declared with no config made no Pub/Sub topic")
	}
}

func liveTopicNamed(t *testing.T, names topics.Names, topic string) string {
	t.Helper()
	name, err := names.Topic(topic)
	if err != nil {
		t.Fatalf("Topic(%s) = %v", topic, err)
	}
	return name
}
