package manifest

import (
	"fmt"
	"testing"
	"time"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestANameThatIsNotLowercaseWordsJoinedByHyphensIsRefused(t *testing.T) {
	t.Parallel()

	for name, declared := range map[string]declaredResource{
		"topic":    topic("Orders", "src/orders.ts:1", nil),
		"task":     task("-resize", "src/resize.ts:1", nil),
		"worker":   worker("media--jobs", "src/media.ts:1", nil),
		"consumer": consumer("email_x", "src/email.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			declarations := []declaredResource{topic("orders", "src/o.ts:1", nil), declared}
			if declared.Type == resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC {
				declarations = []declaredResource{declared}
			}
			_, err := assembleWorkers([]app{{Name: "web"}}, declarations, nil)
			refusedAt(t, err, declared.Source, fmt.Sprintf("%q", declared.Name))
		})
	}
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
