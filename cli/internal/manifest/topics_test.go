package manifest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

func TestATopicCarriesEveryConsumerDeclaredOnIt(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}, {Name: "media"}}, []declaredResource{
		worker("media", "src/media.ts:1", nil),
		topic("orders", "src/orders.ts:1", &resourcesv1.TopicConfig{Ordered: true}),
		consumer("email", "src/email.ts:4", &resourcesv1.ConsumerConfig{Topic: "orders", Worker: "media", Concurrency: 3, Lanes: []topicv1.Lane{topicv1.Lane_LANE_HIGH}}),
		consumer("audit", "src/audit.ts:4", &resourcesv1.ConsumerConfig{Topic: "orders", Worker: "media"}),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	orders := findTopic(m, "topic--orders")
	if orders == nil || !orders.GetOrdered() {
		t.Fatalf("topic--orders = %v, want the ordered topic", orders)
	}
	consumers := orders.GetConsumers()
	if len(consumers) != 2 || consumers[0].GetName() != "audit" || consumers[1].GetName() != "email" {
		t.Fatalf("consumers = %v, want audit and email, in name order", consumers)
	}
	email := consumers[1]
	if email.GetWorker() != "media" || email.GetConcurrency() != 3 || email.GetExclusive() || len(email.GetLanes()) != 1 {
		t.Errorf("email = %v, want its worker, concurrency and lanes, and not exclusive", email)
	}
}

func TestATaskIsATopicWithOneExclusiveConsumer(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		task("resize", "src/resize.ts:3", &resourcesv1.TaskConfig{
			Ttl: durationpb.New(time.Hour), Cron: "*/5 * * * *", Concurrency: 2,
			Retry: &resourcesv1.RetryPolicy{MaxAttempts: 4},
		}),
	}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	var found bool
	for _, r := range m.GetResources() {
		if r.GetLogicalName() != "topic--resize" {
			continue
		}
		found = true
		if r.GetResource().GetType() != resourcesv1.ResourceType_RESOURCE_TYPE_TASK {
			t.Errorf("type = %v, want the task's own type", r.GetResource().GetType())
		}
		resize := r.GetTopic()
		if resize.GetCron() != "*/5 * * * *" || resize.GetTtl().AsDuration() != time.Hour || resize.GetRetry().GetMaxAttempts() != 4 {
			t.Errorf("topic = %v, want the task's cron, ttl and retry", resize)
		}
		consumers := resize.GetConsumers()
		if len(consumers) != 1 || consumers[0].GetName() != "resize" || !consumers[0].GetExclusive() || consumers[0].GetConcurrency() != 2 {
			t.Errorf("consumers = %v, want one exclusive consumer named for the task", consumers)
		}
	}
	if !found {
		t.Fatalf("resources = %v, want the task under topic--resize", m.GetResources())
	}
}

func TestATaskAndATopicOfOneNameAreADuplicateDeclaration(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		topic("orders", "src/a.ts:1", nil),
		task("orders", "src/b.ts:1", nil),
	}, nil)
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) || duplicate.TypeToken != "topic" || duplicate.FirstSource != "src/a.ts:1" || duplicate.SecondSource != "src/b.ts:1" {
		t.Errorf("assemble() = %v, want a DuplicateError in the topic namespace naming both", err)
	}
}

func TestAConsumerOfATopicNothingDeclaresIsRefused(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		consumer("email", "src/email.ts:4", &resourcesv1.ConsumerConfig{Topic: "orders"}),
	}, nil)
	refusedAt(t, err, "src/email.ts:4", `"orders"`)
}

func TestAConsumerOfATaskIsRefused(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		task("resize", "src/resize.ts:3", nil),
		consumer("email", "src/email.ts:4", &resourcesv1.ConsumerConfig{Topic: "resize"}),
	}, nil)
	refusedAt(t, err, "src/email.ts:4", "task")
}

func TestEveryLimitIsRefusedAtTheDeclaringLine(t *testing.T) {
	t.Parallel()

	seconds := func(n int64) *durationpb.Duration { return durationpb.New(time.Duration(n) * time.Second) }
	orderedTopic := topic("orders", "src/orders.ts:1", &resourcesv1.TopicConfig{Ordered: true})
	plainTopic := topic("orders", "src/orders.ts:1", nil)
	for name, tc := range map[string]struct {
		declarations []declaredResource
		words        []string
	}{
		"retry attempts above 100":             {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Retry: &resourcesv1.RetryPolicy{MaxAttempts: 101}})}, []string{"maxAttempts", "100"}},
		"retry attempts below 1":               {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Retry: &resourcesv1.RetryPolicy{MaxAttempts: -1}})}, []string{"maxAttempts"}},
		"retry max delay above 600s":           {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Retry: &resourcesv1.RetryPolicy{MaxDelay: seconds(601)}})}, []string{"maxDelay", "10m"}},
		"retry min delay above max delay":      {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Retry: &resourcesv1.RetryPolicy{MinDelay: seconds(60), MaxDelay: seconds(30)}})}, []string{"minDelay", "maxDelay"}},
		"a topic's retry":                      {[]declaredResource{topic("t", "src/t.ts:1", &resourcesv1.TopicConfig{Retry: &resourcesv1.RetryPolicy{MaxAttempts: 101}})}, []string{"maxAttempts"}},
		"task concurrency above 1000":          {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Concurrency: 1001})}, []string{"concurrency", "1000"}},
		"task concurrency below 1":             {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Concurrency: -2})}, []string{"concurrency"}},
		"consumer concurrency above 1000":      {[]declaredResource{plainTopic, consumer("t", "src/t.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders", Concurrency: 1001})}, []string{"concurrency"}},
		"worker concurrency above 1000":        {[]declaredResource{worker("t", "src/t.ts:1", &resourcesv1.WorkerConfig{Concurrency: 1001})}, []string{"concurrency"}},
		"ttl above 14 days":                    {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Ttl: durationpb.New(15 * 24 * time.Hour)})}, []string{"ttl", "336h"}},
		"ttl of no time":                       {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Ttl: seconds(0)})}, []string{"ttl"}},
		"max duration of no time":              {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{MaxDuration: seconds(0)})}, []string{"maxDuration"}},
		"batch size above 1000":                {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Batch: &resourcesv1.BatchPolicy{Size: 1001}})}, []string{"batch", "1000"}},
		"batch size below 1":                   {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Batch: &resourcesv1.BatchPolicy{}})}, []string{"batch"}},
		"batch timeout above 300s":             {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Batch: &resourcesv1.BatchPolicy{Size: 10, Timeout: seconds(301)}})}, []string{"batch", "5m"}},
		"a batched ordered task":               {[]declaredResource{task("t", "src/t.ts:1", &resourcesv1.TaskConfig{Ordered: true, Batch: &resourcesv1.BatchPolicy{Size: 10}})}, []string{"ordered", "batch"}},
		"a batch above 10 on an ordered topic": {[]declaredResource{orderedTopic, consumer("t", "src/t.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders", Batch: &resourcesv1.BatchPolicy{Size: 11}})}, []string{"ordered", "10"}},
		"a consumer's batch above 1000":        {[]declaredResource{plainTopic, consumer("t", "src/t.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders", Batch: &resourcesv1.BatchPolicy{Size: 1001}})}, []string{"1000"}},
		"a consumer's retry":                   {[]declaredResource{plainTopic, consumer("t", "src/t.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders", Retry: &resourcesv1.RetryPolicy{MaxAttempts: 101}})}, []string{"maxAttempts"}},
		"101 consumers of an ordered topic":    {append([]declaredResource{orderedTopic}, consumersOf("orders", 101)...), []string{"100", "ordered"}},
		"1001 consumers of an unordered topic": {append([]declaredResource{plainTopic}, consumersOf("orders", 1001)...), []string{"1000"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, tc.declarations, nil)
			refusedAt(t, err, tc.declarations[len(tc.declarations)-1].Source, tc.words...)
		})
	}
}

func TestEveryLimitsBoundIsAccepted(t *testing.T) {
	t.Parallel()

	declarations := []declaredResource{
		task("t", "src/t.ts:1", &resourcesv1.TaskConfig{
			Retry:       &resourcesv1.RetryPolicy{MaxAttempts: 100, MinDelay: durationpb.New(10 * time.Minute), MaxDelay: durationpb.New(10 * time.Minute)},
			Concurrency: 1000,
			Ttl:         durationpb.New(14 * 24 * time.Hour),
			Batch:       &resourcesv1.BatchPolicy{Size: 1000, Timeout: durationpb.New(5 * time.Minute)},
		}),
		worker("worker", "src/w.ts:1", &resourcesv1.WorkerConfig{Concurrency: 1000}),
		topic("orders", "src/orders.ts:1", &resourcesv1.TopicConfig{Ordered: true}),
		consumer("c", "src/c.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders", Concurrency: 1, Batch: &resourcesv1.BatchPolicy{Size: 10}}),
	}
	declarations = append(declarations, consumersOf("orders", 99)...)
	if _, err := assembleWorkers([]app{{Name: "web"}}, declarations, nil); err != nil {
		t.Errorf("assemble() = %v, want every bound accepted", err)
	}
}

func consumersOf(topicName string, n int) []declaredResource {
	out := make([]declaredResource, 0, n)
	for i := range n {
		out = append(out, consumer(fmt.Sprintf("c%d", i), fmt.Sprintf("src/c%d.ts:1", i), &resourcesv1.ConsumerConfig{Topic: topicName}))
	}
	return out
}
