package runner

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func TestATeardownsPhaseReachesTheJobAsDestroy(t *testing.T) {
	if got := phaseOf(progressv1.Phase_PHASE_DESTROY); got != runnerv1.JobPhase_JOB_PHASE_DESTROY {
		t.Errorf("phaseOf(destroy) = %v, want the job's destroy phase", got)
	}
}
