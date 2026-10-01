package pgmq

import (
	"context"
	"sync"
	"testing"
	"time"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type reportedAttempts struct {
	mu       sync.Mutex
	attempts []Attempt
}

func (r *reportedAttempts) report(attempt Attempt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
}

func (r *reportedAttempts) all() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.attempts...)
}

func TestEveryAttemptIsReportedWithWhatItLeftTheRunIn(t *testing.T) {
	worker := newWorker(t, failingUntil(2))
	reported := &reportedAttempts{}
	cfg := aDatabase(t)
	cfg.ReportAttempt = reported.report
	engine, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	topics := map[string]*contractv1.ManifestTopic{"resize": aTask(named("resize"), retrying(3, 10*time.Millisecond, 10*time.Millisecond))}
	if err := engine.Apply(context.Background(), Deployment{Slug: "app", Topics: topics, Workers: map[string]Worker{"worker": {URL: worker.server.URL}}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	startDispatch(t, engine)

	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := reported.all()
	if len(got) != 2 {
		t.Fatalf("reported %d attempts (%+v), want the failed first and the completed second", len(got), got)
	}
	for i, want := range []Attempt{
		{Topic: "resize", Consumer: "resize", IsTask: true, Execution: id, Number: 1, MaxAttempts: 3, Status: provider.RunQueued, Reason: "the worker answered 500 Internal Server Error: the disk is full"},
		{Topic: "resize", Consumer: "resize", IsTask: true, Execution: id, Number: 2, MaxAttempts: 3, Status: provider.RunCompleted},
	} {
		attempt := got[i]
		if attempt.Took <= 0 {
			t.Errorf("attempt %d took %v, want how long the worker spent on it", i+1, attempt.Took)
		}
		attempt.Took = 0
		if attempt != want {
			t.Errorf("attempt %d = %+v, want %+v", i+1, attempt, want)
		}
	}
}
