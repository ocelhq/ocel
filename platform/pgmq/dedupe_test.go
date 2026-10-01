package pgmq

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
)

func TestATriggerWithAKnownIdempotencyKeyReturnsTheExistingRun(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)

	first := trigger(t, engine, "resize", `{"n":1}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"})
	second := trigger(t, engine, "resize", `{"n":2}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"})
	other := trigger(t, engine, "resize", `{"n":3}`, &taskv1.TriggerOptions{IdempotencyKey: "order-2"})

	if second != first {
		t.Errorf("the second trigger with key order-1 = %s, want the first run %s", second, first)
	}
	if other == first {
		t.Error("a trigger with another key returned the first run")
	}
	awaitRun(t, engine, first, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	awaitRun(t, engine, other, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	if got := len(worker.received()); got != 2 {
		t.Errorf("the worker received %d runs, want one per key", got)
	}
}

func TestAnIdempotencyKeyIsFreeAgainOnceItsTTLPasses(t *testing.T) {
	engine, _ := aServedTask(t, succeeding)
	options := &taskv1.TriggerOptions{IdempotencyKey: "order-1", IdempotencyKeyTtl: durationpb.New(200 * time.Millisecond)}

	first := trigger(t, engine, "resize", `{}`, options)
	time.Sleep(400 * time.Millisecond)
	if second := trigger(t, engine, "resize", `{}`, options); second == first {
		t.Error("a trigger after the key's ttl returned the earlier run")
	}
}

func TestTriggersWithOneDebounceKeyFoldIntoOneRunDueADelayAfterTheLast(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)
	debounce := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "user-7", Delay: durationpb.New(600 * time.Millisecond)}}

	first := trigger(t, engine, "resize", `{"n":1}`, debounce)
	time.Sleep(300 * time.Millisecond)
	last := time.Now()
	second := trigger(t, engine, "resize", `{"n":2}`, debounce)

	if second != first {
		t.Fatalf("a trigger inside the debounce window = %s, want the pending run %s", second, first)
	}
	if run := retrieve(t, engine, first); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || run.GetDueAt().AsTime().Before(last.Add(600*time.Millisecond)) {
		t.Errorf("run = %v, want it delayed to the window after the last trigger", run)
	}
	awaitRun(t, engine, first, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	got := worker.received()
	if len(got) != 1 {
		t.Fatalf("the worker received %d runs, want the one debounced run", len(got))
	}
	if got[0].at.Before(last.Add(600 * time.Millisecond)) {
		t.Errorf("the run ran %v after the last trigger, want the debounce delay of 600ms", got[0].at.Sub(last))
	}

	if after := trigger(t, engine, "resize", `{"n":3}`, debounce); after == first {
		t.Error("a trigger after the run started joined it, want a new run")
	}
}
