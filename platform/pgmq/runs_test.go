package pgmq

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func aServedTask(t *testing.T, respond func(*topicv1.Envelope) reply, mods ...func(*contractv1.ManifestTopic)) (*Engine, *aWorker) {
	t.Helper()
	worker := newWorker(t, respond)
	return dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(mods...)}, map[string]Worker{"worker": {URL: worker.server.URL}}), worker
}

func retrieve(t *testing.T, engine *Engine, id string) *taskv1.Run {
	t.Helper()
	resp, err := engine.Tasks().RetrieveRun(context.Background(), &taskv1.RetrieveRunRequest{Id: id})
	if err != nil {
		t.Fatalf("RetrieveRun(%s): %v", id, err)
	}
	return resp.GetRun()
}

func TestADelayedRunWaitsUntilItIsDue(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)

	due := time.Now().Add(time.Second)
	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(due)})
	if run := retrieve(t, engine, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || !run.GetDueAt().AsTime().Equal(due.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("run = %v, want delayed until %s", run, due)
	}
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	if at := worker.received()[0].at; at.Before(due) {
		t.Errorf("the worker received the run %v before it was due", due.Sub(at))
	}
}

func TestADelayedRunPastItsDueTimeIsQueuedWhileItWaitsForAWorker(t *testing.T) {
	engine, _ := aServedTask(t, succeeding, func(topic *contractv1.ManifestTopic) {
		topic.Consumers[0].Worker = "elsewhere"
	})

	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(300 * time.Millisecond))})
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_QUEUED)
}

func TestARescheduledRunRunsAtItsNewTime(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)
	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})

	sooner := time.Now().Add(500 * time.Millisecond)
	resp, err := engine.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(sooner)})
	if err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	if !resp.GetRun().GetDueAt().AsTime().Equal(sooner.UTC().Truncate(time.Microsecond)) {
		t.Errorf("RescheduleRun answered due at %v, want %v", resp.GetRun().GetDueAt().AsTime(), sooner)
	}
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
	if at := worker.received()[0].at; at.Before(sooner) {
		t.Errorf("the run ran %v before its new time", sooner.Sub(at))
	}
}

func TestARunThatHasStartedCannotBeRescheduled(t *testing.T) {
	engine, _ := aServedTask(t, succeeding)
	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	_, err := engine.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("RescheduleRun of a completed run = %v, want FailedPrecondition", err)
	}
}

func storedRun(t *testing.T, engine *Engine, run provider.Run) {
	t.Helper()
	run.Topic, run.Consumer, run.CreatedAt = "resize", "resize", time.Now()
	if _, err := engine.Store().WriteRun(context.Background(), run); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
}

func TestARunTheStoreWroteWithoutAQueueMessageCannotBeRescheduled(t *testing.T) {
	engine, _ := aServedTask(t, succeeding)
	storedRun(t, engine, provider.Run{Execution: "stored", Status: provider.RunDelayed, DueAt: time.Now().Add(time.Hour)})

	_, err := engine.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: "stored", DueAt: timestamppb.New(time.Now())})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("RescheduleRun of a run on no queue = %v, want FailedPrecondition", err)
	}
}

func TestARunTheStoreWroteWithoutAQueueMessageExpiresOnceItsTTLPasses(t *testing.T) {
	engine, _ := aServedTask(t, succeeding)
	storedRun(t, engine, provider.Run{Execution: "stored", Status: provider.RunQueued, ExpiresAt: time.Now().Add(-time.Second)})

	awaitRun(t, engine, "stored", taskv1.RunStatus_RUN_STATUS_EXPIRED)
}

func TestACanceledRunThatHasNotStartedNeverRuns(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)
	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(500 * time.Millisecond))})

	resp, err := engine.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: id})
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if resp.GetRun().GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED {
		t.Errorf("CancelRun answered %v, want canceled", resp.GetRun().GetStatus())
	}
	time.Sleep(time.Second)
	if got := len(worker.received()); got != 0 {
		t.Errorf("the worker received %d attempts of a canceled run", got)
	}
	if run := retrieve(t, engine, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED {
		t.Errorf("run = %v, want it still canceled", run.GetStatus())
	}
}

func TestCancelingAnExecutingRunAbortsItsAttemptAndStopsItsRetries(t *testing.T) {
	engine, worker := aServedTask(t, func(*topicv1.Envelope) reply {
		return reply{status: http.StatusInternalServerError, hold: 10 * time.Second}
	},
		retrying(5, 50*time.Millisecond, 50*time.Millisecond))
	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXECUTING)

	started := time.Now()
	if _, err := engine.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: id}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for worker.inFlightNow() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if worker.inFlightNow() > 0 {
		t.Fatal("the worker still holds the attempt of a canceled run")
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("the attempt ran on %v after the cancel", waited)
	}
	time.Sleep(300 * time.Millisecond)
	if got := len(worker.received()); got != 1 {
		t.Errorf("the worker received %d attempts, want only the one canceled", got)
	}
	if run := retrieve(t, engine, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED || run.GetFinishedAt() == nil {
		t.Errorf("run = %v, want canceled and finished", run)
	}
}

func TestAReplayRunsTheSamePayloadAgainAsANewRun(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)
	id := trigger(t, engine, "resize", `{"image":"cat.png"}`, &taskv1.TriggerOptions{Tags: []string{"eu"}})
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	resp, err := engine.Tasks().ReplayRun(context.Background(), &taskv1.ReplayRunRequest{Id: id})
	if err != nil {
		t.Fatalf("ReplayRun: %v", err)
	}
	if resp.GetId() == id {
		t.Fatal("ReplayRun answered the original run's id")
	}
	replayed := awaitRun(t, engine, resp.GetId(), taskv1.RunStatus_RUN_STATUS_COMPLETED)
	if replayed.GetPayload().GetStructValue().GetFields()["image"].GetStringValue() != "cat.png" || len(replayed.GetTags()) != 1 {
		t.Errorf("replayed run = %v, want the original's payload and tags", replayed)
	}
	if got := len(worker.received()); got != 2 {
		t.Errorf("the worker received %d runs, want the original and the replay", got)
	}
}

func TestARunNotStartedWithinItsTTLExpires(t *testing.T) {
	engine, worker := aServedTask(t, succeeding, func(topic *contractv1.ManifestTopic) {
		topic.Consumers[0].Worker = "elsewhere"
	})
	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{Ttl: durationpb.New(300 * time.Millisecond)})

	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXPIRED)
	if run.GetAttempts() != 0 || len(worker.received()) != 0 {
		t.Errorf("run = %v, want it expired without an attempt", run)
	}
}

func TestATaskTTLAppliesToARunThatSetsNone(t *testing.T) {
	engine, _ := aServedTask(t, succeeding, func(topic *contractv1.ManifestTopic) {
		topic.Ttl = durationpb.New(300 * time.Millisecond)
		topic.Consumers[0].Worker = "elsewhere"
	})
	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXPIRED)
}

func TestListingRunsWithACursorNoListingReturnedIsAnInvalidArgument(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, nil)

	_, err := engine.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Task: "resize", Cursor: "not a cursor"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ListRuns with a made-up cursor = %v, want InvalidArgument", err)
	}
}

func TestListingRunsWhenTheDatabaseIsUnreachableIsNotTheCallersFault(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, nil)
	engine.Close()

	_, err := engine.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Task: "resize"})
	if err == nil || connect.CodeOf(err) == connect.CodeInvalidArgument {
		t.Errorf("ListRuns on a closed engine = %v, want an error that does not blame the request", err)
	}
}
