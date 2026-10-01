package pgmq

import (
	"maps"
	"slices"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestADeploymentTakesTheManifestsTopicsAndTheWorkersThatHaveAnAddress(t *testing.T) {
	t.Parallel()

	orders := aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"})
	resize := aTask(named("resize"))
	manifest := &contractv1.Manifest{
		Resources: []*contractv1.ManifestResource{
			{LogicalName: "orders", Config: &contractv1.ManifestResource_Topic{Topic: orders}},
			{LogicalName: "resize", Config: &contractv1.ManifestResource_Topic{Topic: resize}},
			{LogicalName: "main"},
		},
		Workers: []*contractv1.ManifestWorker{{Name: "mail", Concurrency: 4}, {Name: "worker"}, {Name: "idle"}},
	}

	got := DeploymentOf(manifest, map[string]string{"mail": "http://127.0.0.1:4001", "worker": "http://127.0.0.1:4002"})

	if names := slices.Sorted(maps.Keys(got.Topics)); !slices.Equal(names, []string{"orders", "resize"}) || got.Topics["orders"] != orders {
		t.Errorf("topics = %v, want orders and resize as declared", names)
	}
	want := map[string]Worker{"mail": {URL: "http://127.0.0.1:4001", Concurrency: 4}, "worker": {URL: "http://127.0.0.1:4002"}}
	if !maps.Equal(got.Workers, want) {
		t.Errorf("workers = %v, want %v: a worker with no address is not served", got.Workers, want)
	}
}

func TestAQueueNameFitsPgmqsLimitAndStaysDistinctForLongNames(t *testing.T) {
	t.Parallel()

	long := "a-very-long-topic-name-that-goes-on-and-on-for-a-while"
	first, second := queueName(long, "first-consumer"), queueName(long, "second-consumer")
	if len(first) > 47 || len(second) > 47 {
		t.Errorf("queue names %q and %q, want at most pgmq's 47 characters", first, second)
	}
	if first == second {
		t.Errorf("two consumers of one topic share the queue %q", first)
	}
	if got := queueName("orders", "send-email"); got != "orders__send_email" {
		t.Errorf("queueName(orders, send-email) = %q, want orders__send_email", got)
	}
}
