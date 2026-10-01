package pgmq

import (
	"net/http"
	"testing"
	"time"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func holding(*topicv1.Envelope) reply {
	return reply{status: http.StatusOK, hold: 150 * time.Millisecond}
}

func TestATaskRunsAtMostItsConcurrencyAtOnce(t *testing.T) {
	engine, worker := aServedTask(t, holding, func(topic *contractv1.ManifestTopic) { topic.Consumers[0].Concurrency = 2 })

	var ids []string
	for range 6 {
		ids = append(ids, trigger(t, engine, "resize", `{}`, nil))
	}
	for _, id := range ids {
		awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	}
	if peak := worker.peakInFlight(); peak != 2 {
		t.Errorf("the worker held %d runs at once, want the task's concurrency of 2", peak)
	}
}

func TestAWorkerRunsAtMostItsConcurrencyAcrossItsTasksAndConsumers(t *testing.T) {
	worker := newWorker(t, holding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{
		"resize": aTask(),
		"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker"}),
	}, map[string]Worker{"worker": {URL: worker.server.URL, Concurrency: 3}})

	var ids []string
	for range 5 {
		ids = append(ids, trigger(t, engine, "resize", `{}`, nil))
		send(t, engine, "orders", `{}`, nil)
	}
	for _, id := range ids {
		awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	}
	awaitDelivered(t, worker, 10)
	if peak := worker.peakInFlight(); peak != 3 {
		t.Errorf("the worker held %d deliveries at once, want its concurrency of 3", peak)
	}
}
