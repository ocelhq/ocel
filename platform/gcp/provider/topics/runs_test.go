//go:build integration

package topics_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
)

func TestLiveDebouncedTriggersFoldIntoOnePendingRunThatRunsTheFirstPayload(t *testing.T) {
	p := newDelayed(t)
	debounce := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "user-7", Delay: durationpb.New(time.Hour)}}

	first := p.trigger("resize", `{"n":1}`, debounce)
	firstDue := p.retrieve(first).GetDueAt().AsTime()
	time.Sleep(10 * time.Millisecond)
	second := p.trigger("resize", `{"n":2}`, debounce)

	if second != first {
		t.Fatalf("the second debounced trigger answered run %s, want the pending %s", second, first)
	}
	run := p.retrieve(first)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || !run.GetDueAt().AsTime().After(firstDue) {
		t.Errorf("the pending run is %s due %v, want it still delayed and due later than %v", run.GetStatus(), run.GetDueAt().AsTime(), firstDue)
	}
	if string(run.GetPayload()) != `{"n":1}` {
		t.Errorf("the pending run holds %s, want the first trigger's payload", run.GetPayload())
	}
	if tasks := p.delayTasks(); len(tasks) != 2 || !slices.ContainsFunc(tasks, func(task *cloudtaskspb.Task) bool {
		return task.GetScheduleTime().AsTime().Equal(run.GetDueAt().AsTime())
	}) {
		t.Errorf("the delay queue holds %d tasks, want the superseded one and one scheduled for the pending run's due time", len(tasks))
	}

	if _, err := p.deployment.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: first}); err != nil {
		t.Fatal(err)
	}
	if third := p.trigger("resize", `{"n":3}`, debounce); third == first {
		t.Errorf("a debounced trigger after the pending run was canceled answered %s again, want a new run", third)
	}
}

func TestLiveConcurrentDebouncedTriggersAllAnswerTheOnePendingRun(t *testing.T) {
	p := newDelayed(t)
	debounce := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "user-7", Delay: durationpb.New(time.Hour)}}
	const triggers = 32

	ids := make([]string, triggers)
	errs := make([]error, triggers)
	var wg sync.WaitGroup
	for i := range triggers {
		wg.Go(func() {
			resp, err := p.deployment.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "resize", Payload: []byte(`{}`), Options: debounce})
			ids[i], errs[i] = resp.GetId(), err
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("debounced trigger %d = %v, want it folded into the pending run", i, err)
		}
	}
	if distinct := slices.Compact(slices.Sorted(slices.Values(ids))); len(distinct) != 1 {
		t.Errorf("%d concurrent debounced triggers answered runs %v, want one pending run", triggers, distinct)
	}
	due := p.retrieve(ids[0]).GetDueAt().AsTime()
	if tasks := p.delayTasks(); !slices.ContainsFunc(tasks, func(task *cloudtaskspb.Task) bool { return task.GetScheduleTime().AsTime().Equal(due) }) {
		t.Errorf("the delay queue holds %d tasks, want one scheduled for the pending run's due time", len(tasks))
	}
}

func TestLiveAReplayRunsTheSamePayloadAsANewRun(t *testing.T) {
	p := newPublished(t)
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	ctx := context.Background()
	original := p.trigger("resize", exactJSON, &taskv1.TriggerOptions{Tags: []string{"eu"}, MaxAttempts: 1, Key: "k"})
	p.deliverPulled(worker, "resize", "resize")

	resp, err := p.deployment.Tasks().ReplayRun(ctx, &taskv1.ReplayRunRequest{Id: original})
	if err != nil {
		t.Fatalf("ReplayRun() = %v", err)
	}
	if resp.GetId() == original {
		t.Fatal("ReplayRun() answered the original run, want a new one")
	}
	replayed := p.retrieve(resp.GetId())
	if replayed.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || string(replayed.GetPayload()) != exactJSON || !slices.Equal(replayed.GetTags(), []string{"eu"}) {
		t.Errorf("the replay is %s with payload %s and tags %v, want queued with the original's", replayed.GetStatus(), replayed.GetPayload(), replayed.GetTags())
	}
	pulled := p.pull("resize", "resize")
	if len(pulled) != 1 || pulled[0].payload() != exactJSON || pulled[0].Message.Attributes["ocel-max-attempts"] != "1" {
		t.Errorf("the replay published %d messages, want one with the original payload and its maxAttempts", len(pulled))
	}
	if _, err := p.deployment.Tasks().ReplayRun(ctx, &taskv1.ReplayRunRequest{Id: "nothing"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("ReplayRun() of no run = %v, want %s", err, connect.CodeNotFound)
	}
}

func TestLiveBatchTriggerAnswersARunForEachItemInOrder(t *testing.T) {
	p := newPublished(t)

	resp, err := p.deployment.Tasks().BatchTrigger(context.Background(), &taskv1.BatchTriggerRequest{Task: "resize", Items: []*taskv1.BatchTriggerItem{
		{Payload: []byte(`{"n":1}`)}, {Payload: []byte(`{"n":2}`)},
	}})
	if err != nil {
		t.Fatalf("BatchTrigger() = %v", err)
	}
	if len(resp.GetIds()) != 2 {
		t.Fatalf("BatchTrigger() answered %d runs, want 2", len(resp.GetIds()))
	}
	for i, id := range resp.GetIds() {
		if run := p.retrieve(id); string(run.GetPayload()) != `{"n":`+string(rune('1'+i))+`}` {
			t.Errorf("run %d holds %s, want item %d's payload", i, run.GetPayload(), i)
		}
	}
}

func TestLiveABatchWithAnInvalidItemTriggersNone(t *testing.T) {
	p := newPublished(t)

	_, err := p.deployment.Tasks().BatchTrigger(context.Background(), &taskv1.BatchTriggerRequest{Task: "resize", Items: []*taskv1.BatchTriggerItem{
		{Payload: []byte(`{"n":1}`)}, {Payload: []byte(`{`)},
	}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("BatchTrigger() with a payload that is not JSON = %v, want %s", err, connect.CodeInvalidArgument)
	}
	if pulled := p.pull("resize", "resize"); len(pulled) != 0 {
		t.Errorf("a refused batch published %d messages, want none", len(pulled))
	}
}

func TestLiveABatchThatFailsPartWayAnswersTheRunsItTriggered(t *testing.T) {
	p := newPublished(t)

	resp, err := p.deployment.Tasks().BatchTrigger(context.Background(), &taskv1.BatchTriggerRequest{Task: "resize", Items: []*taskv1.BatchTriggerItem{
		{Payload: []byte(`{"n":1}`)}, {Payload: []byte(`{"n":2}`), Options: &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))}},
	}})
	if err == nil {
		t.Fatal("BatchTrigger() of a delayed item with no delay queue answered no error")
	}
	if len(resp.GetIds()) != 1 || !strings.Contains(err.Error(), resp.GetIds()[0]) {
		t.Fatalf("BatchTrigger() answered runs %v and %v, want the first item's run in both", resp.GetIds(), err)
	}
	if run := p.retrieve(resp.GetIds()[0]); string(run.GetPayload()) != `{"n":1}` {
		t.Errorf("the triggered run holds %s, want the first item's payload", run.GetPayload())
	}
}

func TestLiveRunsAreListedByStatusAndTag(t *testing.T) {
	p := newPublished(t)
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	done := p.trigger("resize", `{}`, &taskv1.TriggerOptions{Tags: []string{"eu"}})
	p.deliverPulled(worker, "resize", "resize")
	waiting := p.trigger("resize", `{}`, &taskv1.TriggerOptions{Tags: []string{"eu"}})

	resp, err := p.deployment.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Task: "resize", Statuses: []taskv1.RunStatus{taskv1.RunStatus_RUN_STATUS_COMPLETED}, Tags: []string{"eu"}})
	if err != nil {
		t.Fatalf("ListRuns() = %v", err)
	}
	if len(resp.GetRuns()) != 1 || resp.GetRuns()[0].GetId() != done {
		t.Errorf("ListRuns(completed) = %d runs, want only %s and not the queued %s", len(resp.GetRuns()), done, waiting)
	}
	if _, err := p.deployment.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Cursor: "made-up"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ListRuns() with a made-up cursor = %v, want %s", err, connect.CodeInvalidArgument)
	}
}

func TestLiveATriggerPastItsTTLReadsAsExpired(t *testing.T) {
	p := newPublished(t)

	id := p.trigger("resize", `{}`, &taskv1.TriggerOptions{Ttl: durationpb.New(time.Millisecond)})
	time.Sleep(20 * time.Millisecond)

	if run := p.retrieve(id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_EXPIRED {
		t.Errorf("a run past its ttl that never started reads as %s, want expired", run.GetStatus())
	}
}
