package terminal

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAHeartbeatForASpanInNoPhaseNamesTheSpanAlone(t *testing.T) {
	t.Parallel()

	run, sink, ticks, out, c := heartbeatRun(t)
	run.Phase(progressv1.Phase_PHASE_UNSPECIFIED).Child("", progress.Title{Started: "Resolving the app's environment", Ended: "Resolved the app's environment"})
	ticks <- c.now().Add(30 * time.Second)

	want := "INFO  Still running: Resolving the app's environment — 0/1 done, 30s elapsed\n" +
		"WARN  Resolving the app's environment did not finish\n"
	if got := closed(t, sink, out); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
