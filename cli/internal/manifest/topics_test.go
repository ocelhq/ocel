package manifest

import (
	"errors"
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
