package runs_test

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
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestADelayPastTheLongestAnOrderedTopicHoldsIsRefusedWithNothingRecordedOrSent(t *testing.T) {
	t.Parallel()

	topics := aTaskNamed("ledger", 3, true)
	topics["audit"] = aTaskNamed("audit", 3, false)["audit"]
	topics["accounts"] = &provider.TopicSpec{Ordered: true, Consumers: []provider.ConsumerSpec{{Name: "apply", Worker: "w", Retry: provider.RetryPolicy{MaxAttempts: 3}}}}
	p := aPushedEngine(t, topics, http.StatusOK, func(cfg *runs.Config) { cfg.MaxOrderedDelay = time.Hour })
	ctx := context.Background()
	tooLate := timestamppb.New(time.Now().Add(2 * time.Hour))

	for name, options := range map[string]*taskv1.TriggerOptions{
		"due past the limit":       {DueAt: tooLate},
		"debounced past the limit": {Debounce: &taskv1.Debounce{Key: "k", Delay: durationpb.New(2 * time.Hour)}},
		"with an idempotency key":  {DueAt: tooLate, IdempotencyKey: "once"},
	} {
		_, err := p.engine.Tasks().Trigger(ctx, &taskv1.TriggerRequest{Task: "ledger", Payload: []byte(`{}`), Options: options})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("a trigger of an ordered task %s = %v, want it refused as an invalid argument", name, err)
		}
	}
	if _, err := p.engine.Topics().Send(ctx, &topicv1.SendRequest{Topic: "accounts", Payload: []byte(`{}`), DueAt: tooLate}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a send to an ordered topic due past the limit = %v, want it refused as an invalid argument", err)
	}
	if count, err := p.store.CountRuns(ctx, runs.RunFilter{Topics: []string{"ledger", "accounts"}}); err != nil || count != 0 || len(p.queue.Sent()) != 0 {
		t.Fatalf("refused delays left %d runs (%v) and %d sent messages, want none", count, err, len(p.queue.Sent()))
	}
	first := p.trigger(t, "ledger", &taskv1.TriggerOptions{IdempotencyKey: "once"})
	if again := p.trigger(t, "ledger", &taskv1.TriggerOptions{IdempotencyKey: "once"}); again != first {
		t.Fatalf("the idempotency key a refused trigger named answered %s, then %s, want the run it first started", first, again)
	}

	id := p.trigger(t, "ledger", &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(30 * time.Minute))})
	before, err := p.store.ReadRun(ctx, "ledger", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.engine.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: tooLate}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("rescheduling an ordered task's run past the limit = %v, want it refused as an invalid argument", err)
	}
	if after, err := p.store.ReadRun(ctx, "ledger", id); err != nil || !after.DueAt.Equal(before.DueAt) || after.Delivery != before.Delivery {
		t.Errorf("a refused reschedule left the run due at %v (%v), want it due at %v", after.DueAt, err, before.DueAt)
	}

	if _, err := p.engine.Tasks().Trigger(ctx, &taskv1.TriggerRequest{Task: "audit", Payload: []byte(`{}`), Options: &taskv1.TriggerOptions{DueAt: tooLate}}); err != nil {
		t.Errorf("a trigger of an unordered task due past the ordered limit: %v", err)
	}
}

func TestRunsAreListedByTheStatusTheyReadAsRatherThanTheOneLastStored(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK, nil)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	for execution, run := range map[string]runs.Run{
		"due":     {Status: provider.RunDelayed, DueAt: past},
		"lapsed":  {Status: provider.RunQueued, DueAt: past, ExpiresAt: past.Add(time.Second)},
		"waiting": {Status: provider.RunDelayed, DueAt: time.Now().Add(time.Hour)},
	} {
		run.Execution, run.Topic, run.Consumer, run.MaxAttempts, run.CreatedAt = runs.ExecutionOf(runs.NewMessageID(past), "remind")+execution, "remind", "remind", 3, past
		if _, err := p.store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	for status, want := range map[taskv1.RunStatus]int{
		taskv1.RunStatus_RUN_STATUS_QUEUED:  1,
		taskv1.RunStatus_RUN_STATUS_EXPIRED: 1,
		taskv1.RunStatus_RUN_STATUS_DELAYED: 1,
	} {
		listed, err := p.engine.Tasks().ListRuns(ctx, &taskv1.ListRunsRequest{Task: "remind", Statuses: []taskv1.RunStatus{status}})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.GetRuns()) != want {
			t.Errorf("ListRuns(%s) = %d runs, want %d", status, len(listed.GetRuns()), want)
		}
		for _, run := range listed.GetRuns() {
			if run.GetStatus() != status {
				t.Errorf("ListRuns(%s) listed a run that reads %s", status, run.GetStatus())
			}
		}
	}
}
