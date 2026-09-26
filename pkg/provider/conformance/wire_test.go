package conformance

import (
	"strings"
	"testing"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func planned() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Plan{Plan: &planv1.ChangePlan{}}}
}

func logged() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{Log: &progressv1.LogEvent{Message: "working"}}}
}

func progressed() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{Message: "working"}}}
}

func observed(events ...*progressv1.OperationEvent) streamed {
	var seen streamed
	for _, event := range events {
		seen.observe(event)
	}
	return seen
}

func TestARunThatOnlyLogsReportsNoProgress(t *testing.T) {
	t.Parallel()

	found := faults(observed(planned()), observed(planned(), logged()))
	if len(found) != 1 || !strings.Contains(found[0], "no progress") {
		t.Fatalf("the tier found %v against a run that only logged, want it failed for reporting no progress", found)
	}
}

func TestARunThatSaysWhatItWouldChangeAndThenReportsProgressPasses(t *testing.T) {
	t.Parallel()

	if found := faults(observed(planned()), observed(planned(), logged(), progressed())); len(found) != 0 {
		t.Fatalf("the tier found %v against a run that showed its plan and then reported progress", found)
	}
}

func TestARunThatShowsNoPlanFails(t *testing.T) {
	t.Parallel()

	found := faults(observed(), observed(progressed()))
	if len(found) != 2 {
		t.Fatalf("the tier found %v against a run that showed no plan on either stream, want both streams failed", found)
	}
}

func TestADryRunThatWorksFails(t *testing.T) {
	t.Parallel()

	found := faults(observed(planned(), progressed()), observed(planned(), progressed()))
	if len(found) != 1 || !strings.Contains(found[0], "dry run changes nothing") {
		t.Fatalf("the tier found %v against a dry run that reported work, want it failed for working", found)
	}
}
