package pgmq

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func batching(size int32, timeout time.Duration) *contractv1.ManifestConsumer {
	return &contractv1.ManifestConsumer{Name: "digest", Worker: "worker", Batch: &resourcesv1.BatchPolicy{Size: size, Timeout: durationpb.New(timeout)}}
}

func batchesOf(got []delivered) [][]int {
	var batches [][]int
	for _, d := range got {
		var batch []int
		for _, m := range d.envelope.GetMessages() {
			batch = append(batch, int(m.GetPayload().GetStructValue().GetFields()["n"].GetNumberValue()))
		}
		batches = append(batches, batch)
	}
	return batches
}

func TestABatchConsumerReceivesUpToItsSizeInOneEnvelope(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(batching(3, time.Second))}, map[string]Worker{"worker": {URL: worker.server.URL}})
	for n := range 7 {
		send(t, engine, "orders", fmt.Sprintf(`{"n":%d}`, n), nil)
	}

	deadline := time.Now().Add(10 * time.Second)
	var all []int
	for len(all) < 7 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		all = slices.Concat(batchesOf(worker.received())...)
	}
	slices.Sort(all)
	if !slices.Equal(all, []int{0, 1, 2, 3, 4, 5, 6}) {
		t.Fatalf("the worker received %v across its batches, want each of the 7 messages once", all)
	}
	batches := batchesOf(worker.received())
	for _, d := range worker.received() {
		if d.envelope.GetExecution() != "" || d.envelope.GetConsumer() != "digest" {
			t.Errorf("batch envelope = %v, want the consumer and no single execution", d.envelope)
		}
		for _, m := range d.envelope.GetMessages() {
			if m.GetExecution() != m.GetMessage().GetId()+"-digest" || m.GetAttempt().GetNumber() != 1 {
				t.Errorf("delivery = %v, want its own execution and attempt", m)
			}
		}
	}
	if len(batches) > 4 || len(batches[0]) != 3 {
		t.Errorf("batches = %v, want full batches of 3 while messages wait", batches)
	}
}

func TestABatchShorterThanItsSizeIsSentOnceItsTimeoutPasses(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(batching(10, 500*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	sent := time.Now()
	send(t, engine, "orders", `{"n":1}`, nil)
	got := awaitDelivered(t, worker, 1)

	if waited := got[0].at.Sub(sent); waited < 400*time.Millisecond {
		t.Errorf("a batch of 1 went out %v after the send, want it held for the 500ms timeout", waited)
	}
	if len(got[0].envelope.GetMessages()) != 1 {
		t.Errorf("batch = %v, want the one message", batchesOf(got))
	}
}

func TestAFailedBatchIsRetriedWhole(t *testing.T) {
	worker := newWorker(t, failingUntil(2))
	orders := aTopic(batching(3, 300*time.Millisecond))
	orders.Retry = &resourcesv1.RetryPolicy{MaxAttempts: 3, MinDelay: durationpb.New(100 * time.Millisecond), MaxDelay: durationpb.New(100 * time.Millisecond)}
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": orders}, map[string]Worker{"worker": {URL: worker.server.URL}})
	for n := range 3 {
		send(t, engine, "orders", fmt.Sprintf(`{"n":%d}`, n), nil)
	}

	got := awaitDelivered(t, worker, 2)
	if first, second := batchesOf(got)[0], batchesOf(got)[1]; len(first) != 3 || !slices.Equal(first, second) {
		t.Errorf("batches = %v, want the failed batch of 3 again", batchesOf(got))
	}
	for _, m := range got[1].envelope.GetMessages() {
		if m.GetAttempt().GetNumber() != 2 {
			t.Errorf("retried delivery = %v, want attempt 2", m.GetAttempt())
		}
	}
}

func TestAnOrderedBatchHoldsAKeysMessagesInSendOrder(t *testing.T) {
	worker := newWorker(t, succeeding)
	orders := aTopic(batching(3, 300*time.Millisecond))
	orders.Ordered = true
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": orders}, map[string]Worker{"worker": {URL: worker.server.URL}})
	for n := range 6 {
		send(t, engine, "orders", fmt.Sprintf(`{"n":%d}`, n), &topicv1.SendRequest{Key: "customer-1"})
	}

	deadline := time.Now().Add(10 * time.Second)
	var all []int
	for len(all) < 6 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		all = slices.Concat(batchesOf(worker.received())...)
	}
	if !slices.Equal(all, []int{0, 1, 2, 3, 4, 5}) {
		t.Errorf("one key's messages arrived as %v, want send order", batchesOf(worker.received()))
	}
}
