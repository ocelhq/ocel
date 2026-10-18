package runs_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

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
