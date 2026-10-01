package pgmq

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func failingUntil(attempt int32) func(*topicv1.Envelope) reply {
	return func(envelope *topicv1.Envelope) reply {
		number := envelope.GetAttempt().GetNumber()
		if len(envelope.GetMessages()) > 0 {
			number = envelope.GetMessages()[0].GetAttempt().GetNumber()
		}
		if number < attempt {
			return reply{status: http.StatusInternalServerError, body: "the disk is full"}
		}
		return reply{status: http.StatusOK, body: `"resized"`}
	}
}

func TestTheBackoffDoublesFromTheMinimumDelayUpToTheMaximumWithJitterInItsUpperHalf(t *testing.T) {
	t.Parallel()

	policy := retryPolicy{maxAttempts: 10, minDelay: time.Second, maxDelay: 5 * time.Second}
	for _, tc := range []struct {
		attempt int
		random  float64
		want    time.Duration
	}{
		{1, 1, time.Second},
		{1, 0, 500 * time.Millisecond},
		{2, 1, 2 * time.Second},
		{3, 1, 4 * time.Second},
		{4, 1, 5 * time.Second},
		{4, 0, 2500 * time.Millisecond},
		{30, 1, 5 * time.Second},
	} {
		if got := policy.backoff(tc.attempt, tc.random); got != tc.want {
			t.Errorf("backoff(attempt %d, random %v) = %v, want %v", tc.attempt, tc.random, got, tc.want)
		}
	}
}

func TestAFailedAttemptIsRetriedAfterABackoffUntilOneSucceeds(t *testing.T) {
	worker := newWorker(t, failingUntil(3))
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(5, 200*time.Millisecond, 400*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := worker.received()
	if len(got) != 3 || run.GetAttempts() != 3 {
		t.Fatalf("the worker received %d attempts and the run counts %d, want 3", len(got), run.GetAttempts())
	}
	first := got[0].envelope.GetAttempt().GetFirstAttemptedAt().AsTime()
	for i, d := range got {
		if a := d.envelope.GetAttempt(); a.GetNumber() != int32(i+1) || a.GetOf() != 5 || !a.GetFirstAttemptedAt().AsTime().Equal(first) {
			t.Errorf("attempt %d = %v, want number %d of 5 with the first attempt's time", i, a, i+1)
		}
	}
	if gap := got[1].at.Sub(got[0].at); gap < 100*time.Millisecond {
		t.Errorf("the second attempt came %v after the first, want at least half the 200ms minimum delay", gap)
	}
	if run.GetError() != "" {
		t.Errorf("a completed run keeps the error %q of an earlier attempt", run.GetError())
	}
}

func TestARunThatFailsEveryAttemptFailsWithTheLastError(t *testing.T) {
	worker := newWorker(t, failingUntil(100))
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(5, 50*time.Millisecond, 50*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{MaxAttempts: 2})
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_FAILED)

	if got := len(worker.received()); got != 2 || run.GetAttempts() != 2 {
		t.Errorf("the worker received %d attempts and the run counts %d, want the 2 the trigger lowered maxAttempts to", got, run.GetAttempts())
	}
	if !strings.Contains(run.GetError(), "500") || !strings.Contains(run.GetError(), "the disk is full") {
		t.Errorf("error = %q, want the worker's last answer", run.GetError())
	}
}

func TestATriggerCannotRaiseTheTasksMaxAttempts(t *testing.T) {
	worker := newWorker(t, failingUntil(100))
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(2, 50*time.Millisecond, 50*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, &taskv1.TriggerOptions{MaxAttempts: 9})
	if run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_FAILED); run.GetAttempts() != 2 {
		t.Errorf("attempts = %d, want the task's 2", run.GetAttempts())
	}
}

func TestAnAbortFailsTheRunWithoutRetrying(t *testing.T) {
	answer, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "no such image"}}})
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorker(t, func(*topicv1.Envelope) reply {
		return reply{status: http.StatusUnprocessableEntity, body: string(answer)}
	})
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(5, 50*time.Millisecond, 50*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_FAILED)

	if run.GetAttempts() != 1 || len(worker.received()) != 1 {
		t.Errorf("attempts = %d, want the 1 that aborted", run.GetAttempts())
	}
	if run.GetError() != "no such image" {
		t.Errorf("error = %q, want the abort's reason", run.GetError())
	}
}

func TestAnAttemptPastMaxDurationTimesTheRunOutWithoutRetrying(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 5 * time.Second} })
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask(retrying(5, 50*time.Millisecond, 50*time.Millisecond), func(topic *contractv1.ManifestTopic) {
		topic.Consumers[0].MaxDuration = durationpb.New(300 * time.Millisecond)
	})}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_TIMED_OUT)

	if run.GetAttempts() != 1 || len(worker.received()) != 1 {
		t.Errorf("attempts = %d, want 1", run.GetAttempts())
	}
}
