package topic

import (
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/pgmq"
)

func describeAttempt(attempt pgmq.Attempt) string {
	subject := fmt.Sprintf("consumer %q of topic %q execution %s", attempt.Consumer, attempt.Topic, attempt.Execution)
	if attempt.Task {
		subject = fmt.Sprintf("task %q run %s", attempt.Topic, attempt.Execution)
	}
	verb := "failed"
	switch attempt.Status {
	case provider.RunCompleted:
		verb = "completed"
	case provider.RunTimedOut:
		verb = "timed out"
	}
	line := fmt.Sprintf("%s %s in %s (attempt %d of %d)", subject, verb, attempt.Took.Round(time.Millisecond), attempt.Number, attempt.Of)
	if attempt.Status == provider.RunQueued {
		line += ", and is retried"
	}
	if attempt.Reason != "" {
		line += ": " + attempt.Reason
	}
	return line
}
