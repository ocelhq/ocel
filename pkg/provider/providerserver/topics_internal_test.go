package providerserver

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func topicManifestResource(typ resourcesv1.ResourceType, name string, topic *contractv1.ManifestTopic) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "topic--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: typ, Name: name},
		Config:      &contractv1.ManifestResource_Topic{Topic: topic},
	}
}

func resolvedRetry(attempts int32, minDelay, maxDelay time.Duration) *resourcesv1.RetryPolicy {
	return &resourcesv1.RetryPolicy{MaxAttempts: attempts, MinDelay: durationpb.New(minDelay), MaxDelay: durationpb.New(maxDelay)}
}

func TestATopicIsHandedToTheVendorWithEachConsumerAsTheManifestDeclaresIt(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(topicManifestResource(resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, "orders", &contractv1.ManifestTopic{
		Schema:  "sha256-abc",
		Ordered: false,
		Consumers: []*contractv1.ManifestConsumer{
			{
				Name:        "email",
				Worker:      "worker",
				Retry:       resolvedRetry(5, 2*time.Second, 90*time.Second),
				Concurrency: 20,
				MaxDuration: durationpb.New(5 * time.Minute),
				Lanes:       []topicv1.Lane{topicv1.Lane_LANE_HIGH, topicv1.Lane_LANE_LOW},
			},
			{
				Name:   "ledger",
				Worker: "media",
				Retry:  resolvedRetry(3, time.Second, time.Minute),
				Batch:  &resourcesv1.BatchPolicy{Size: 50, Timeout: durationpb.New(10 * time.Second)},
			},
		},
	}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	want := &provider.TopicSpec{
		Schema: "sha256-abc",
		Consumers: []provider.ConsumerSpec{
			{
				Name:        "email",
				Worker:      "worker",
				Retry:       provider.RetryPolicy{MaxAttempts: 5, MinDelay: 2 * time.Second, MaxDelay: 90 * time.Second},
				Concurrency: 20,
				MaxDuration: 5 * time.Minute,
				Lanes:       []provider.Lane{provider.LaneHigh, provider.LaneLow},
			},
			{
				Name:   "ledger",
				Worker: "media",
				Retry:  provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute},
				Batch:  &provider.BatchPolicy{Size: 50, Timeout: 10 * time.Second},
			},
		},
	}
	if resource.Type != provider.BindingTopic {
		t.Errorf("Type = %q, want %q", resource.Type, provider.BindingTopic)
	}
	if !reflect.DeepEqual(resource.Topic, want) {
		t.Errorf("Topic = %+v, want %+v", resource.Topic, want)
	}
}

func TestATaskIsHandedToTheVendorWithItsScheduleTTLAndOneExclusiveConsumer(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(topicManifestResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "nightly-report", &contractv1.ManifestTopic{
		Ordered:   true,
		Ttl:       durationpb.New(time.Hour),
		Cron:      "0 3 * * *",
		Consumers: []*contractv1.ManifestConsumer{{Name: "nightly-report", Worker: "worker", Exclusive: true, Retry: resolvedRetry(3, time.Second, time.Minute)}},
	}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	want := &provider.TopicSpec{
		Ordered:   true,
		TTL:       time.Hour,
		Cron:      "0 3 * * *",
		Consumers: []provider.ConsumerSpec{{Name: "nightly-report", Worker: "worker", Exclusive: true, Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute}}},
	}
	if resource.Type != provider.BindingTask {
		t.Errorf("Type = %q, want %q", resource.Type, provider.BindingTask)
	}
	if !reflect.DeepEqual(resource.Topic, want) {
		t.Errorf("Topic = %+v, want %+v", resource.Topic, want)
	}
}

func TestAConsumerNamingNoRetryRunsTheTopicsPolicyOverTheDefaults(t *testing.T) {
	t.Parallel()

	resource, err := manifestResource(topicManifestResource(resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, "orders", &contractv1.ManifestTopic{
		Retry:     &resourcesv1.RetryPolicy{MaxAttempts: 7},
		Consumers: []*contractv1.ManifestConsumer{{Name: "email", Worker: "worker"}},
	}))
	if err != nil {
		t.Fatalf("manifestResource: %v", err)
	}
	want := provider.RetryPolicy{MaxAttempts: 7, MinDelay: provider.DefaultRetryMinDelay, MaxDelay: provider.DefaultRetryMaxDelay}
	if got := resource.Topic.Consumers[0].Retry; got != want {
		t.Errorf("Retry = %+v, want %+v", got, want)
	}
}

func TestATopicDeclaringWhatNoVendorCanRunIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	consumer := func(edit func(*contractv1.ManifestConsumer)) *contractv1.ManifestConsumer {
		c := &contractv1.ManifestConsumer{Name: "email", Worker: "worker"}
		edit(c)
		return c
	}
	for _, tc := range []struct {
		name  string
		typ   resourcesv1.ResourceType
		topic *contractv1.ManifestTopic
		says  string
	}{
		{name: "a consumer with no name", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Name = "" })}}, says: "consumer name"},
		{name: "a consumer with no worker", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Worker = "" })}}, says: "worker"},
		{name: "a consumer declared twice", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(*contractv1.ManifestConsumer) {}), consumer(func(*contractv1.ManifestConsumer) {})}}, says: `"email" twice`},
		{name: "an undefined lane", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Lanes = []topicv1.Lane{topicv1.Lane_LANE_UNSPECIFIED} })}}, says: "lane"},
		{name: "a negative concurrency", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Concurrency = -1 })}}, says: "concurrency -1"},
		{name: "a negative max duration", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.MaxDuration = durationpb.New(-time.Second) })}}, says: "maxDuration"},
		{name: "an empty batch", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Batch = &resourcesv1.BatchPolicy{} })}}, says: "batch size 0"},
		{name: "a negative ttl", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, topic: &contractv1.ManifestTopic{Ttl: durationpb.New(-time.Second), Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Exclusive = true })}}, says: "ttl"},
		{name: "an invalid schedule", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, topic: &contractv1.ManifestTopic{Cron: "every day", Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Exclusive = true })}}, says: `"every day"`},
		{name: "a schedule on a topic", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Cron: "0 3 * * *", Consumers: []*contractv1.ManifestConsumer{consumer(func(*contractv1.ManifestConsumer) {})}}, says: "schedule"},
		{name: "a task with no consumer", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, topic: &contractv1.ManifestTopic{}, says: "one exclusive consumer"},
		{name: "a task whose consumer is shared", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(*contractv1.ManifestConsumer) {})}}, says: "one exclusive consumer"},
		{name: "a topic with an exclusive consumer", typ: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, topic: &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{consumer(func(c *contractv1.ManifestConsumer) { c.Exclusive = true })}}, says: "exclusive"},
		{name: "a topic config on a bucket", typ: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, topic: &contractv1.ManifestTopic{}, says: "bucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := manifestResource(topicManifestResource(tc.typ, "orders", tc.topic))
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("manifestResource() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("manifestResource() = %q, want it to say %q", err, tc.says)
			}
		})
	}
}
