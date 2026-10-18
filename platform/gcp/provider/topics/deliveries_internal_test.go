package topics

import (
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/provider"
)

func aDeliveredRun(maxAttempts int) pushedRun {
	at := time.Now().UTC()
	return pushedRun{
		topicName:   "orders",
		topic:       &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "ship"}}},
		consumer:    provider.ConsumerSpec{Name: "ship"},
		messageID:   envelope.NewMessageID(at),
		publishedAt: at,
		dueAt:       at,
		payload:     []byte(`{"order":7}`),
		maxAttempts: maxAttempts,
	}
}

func aRunAt(status provider.RunStatus, attempts int, delivered pushedRun) storedRun {
	var run storedRun
	startStoredRun(&run, delivered)
	run.Execution, run.Status, run.Attempts, run.StartedAt = delivered.execution(), status, attempts, delivered.publishedAt
	return run
}

func TestARedeliveryOfARunWhoseAttemptsRanOutSettlesItFailedWithoutRunningItAgain(t *testing.T) {
	t.Parallel()
	delivered := aDeliveredRun(3)
	run := aRunAt(provider.RunExecuting, 3, delivered)
	now := time.Now()

	if err := claimRun(&run, true, delivered, now); err != nil {
		t.Fatalf("claimRun() = %v, want the run settled", err)
	}

	if run.Status != provider.RunFailed || run.Attempts != 3 {
		t.Fatalf("the run is %s after %d attempts, want it failed after its 3", run.Status, run.Attempts)
	}
	if !strings.Contains(run.Error, "attempts") || !run.FinishedAt.Equal(now) {
		t.Errorf("the run failed with %q at %v, want a reason naming its spent attempts at %v", run.Error, run.FinishedAt, now)
	}
	if string(run.Payload) != `{"order":7}` {
		t.Errorf("the failed run keeps payload %s, want the message's to redrive", run.Payload)
	}
}

func TestARedeliveryOfARunLeftExecutingCountsAsItsNextAttempt(t *testing.T) {
	t.Parallel()
	delivered := aDeliveredRun(3)
	run := aRunAt(provider.RunExecuting, 1, delivered)

	if err := claimRun(&run, true, delivered, time.Now()); err != nil {
		t.Fatalf("claimRun() = %v, want the run claimed", err)
	}

	if run.Status != provider.RunExecuting || run.Attempts != 2 {
		t.Errorf("the run is %s on attempt %d, want it executing its second", run.Status, run.Attempts)
	}
}
