package queue

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/pgmq"
)

func TestAnAttemptIsDescribedByWhatItRanAndWhatBecameOfTheRun(t *testing.T) {
	t.Parallel()
	task := pgmq.Attempt{Topic: "greet", Consumer: "greet", IsTask: true, Execution: "01JZ-greet", Number: 1, MaxAttempts: 3, Took: 12 * time.Millisecond}
	consumer := pgmq.Attempt{Topic: "orders", Consumer: "audit", Execution: "01JZ-audit", Number: 3, MaxAttempts: 3, Took: 1500 * time.Millisecond}
	for _, tc := range []struct {
		attempt pgmq.Attempt
		status  provider.RunStatus
		reason  string
		want    string
	}{
		{task, provider.RunCompleted, "", `task "greet" run 01JZ-greet completed in 12ms (attempt 1 of 3)`},
		{task, provider.RunQueued, "the disk is full", `task "greet" run 01JZ-greet failed in 12ms (attempt 1 of 3), and is retried: the disk is full`},
		{consumer, provider.RunFailed, "boom", `consumer "audit" of topic "orders" execution 01JZ-audit failed in 1.5s (attempt 3 of 3): boom`},
		{task, provider.RunTimedOut, "the attempt ran past its maxDuration", `task "greet" run 01JZ-greet timed out in 12ms (attempt 1 of 3): the attempt ran past its maxDuration`},
	} {
		tc.attempt.Status, tc.attempt.Reason = tc.status, tc.reason
		if got := describeAttempt(tc.attempt); got != tc.want {
			t.Errorf("describeAttempt(%s) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
