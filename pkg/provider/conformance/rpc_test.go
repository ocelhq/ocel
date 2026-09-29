package conformance

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func stamped(event *progressv1.OperationEvent) *progressv1.OperationEvent {
	event.Time = timestamppb.Now()
	event.Level = progressv1.Level_LEVEL_INFO
	return event
}

func planned() *progressv1.OperationEvent {
	return stamped(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Plan{Plan: &planv1.ChangePlan{}}})
}

var scope = []byte("provisn1")

func logged() *progressv1.OperationEvent {
	return stamped(&progressv1.OperationEvent{
		SpanId:  scope,
		Message: "working",
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{}},
	})
}

func progressed() *progressv1.OperationEvent {
	return stamped(&progressv1.OperationEvent{SpanId: scope, Message: "working"})
}

func started(id string) *progressv1.OperationEvent {
	return stamped(&progressv1.OperationEvent{
		SpanId:  []byte(id),
		Message: id,
		Body:    &progressv1.OperationEvent_Started{Started: &progressv1.Started{}},
	})
}

func ended(id string) *progressv1.OperationEvent {
	return stamped(&progressv1.OperationEvent{
		SpanId: []byte(id),
		Body:   &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{}},
	})
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

func TestARunThatSendsAnEventWithNoTimeOrLevelFails(t *testing.T) {
	t.Parallel()

	timeless := progressed()
	timeless.Time = nil
	unleveled := logged()
	unleveled.Level = progressv1.Level_LEVEL_UNSPECIFIED

	for name, event := range map[string]*progressv1.OperationEvent{"no time": timeless, "no level": unleveled} {
		found := faults(observed(planned()), observed(planned(), progressed(), event))
		if len(found) != 1 || !strings.Contains(found[0], "time and a level") {
			t.Errorf("the tier found %v against a run that sent an event with %s, want it failed for an unstamped event", found, name)
		}
	}
}

func TestAWarningIsNotProgress(t *testing.T) {
	t.Parallel()

	warned := progressed()
	warned.Level = progressv1.Level_LEVEL_WARN
	found := faults(observed(planned()), observed(planned(), warned))
	if len(found) != 1 || !strings.Contains(found[0], "no progress") {
		t.Fatalf("the tier found %v against a run that only warned, want it failed for reporting no progress", found)
	}
}

func TestARunThatStartsAScopeItNeverEndsFails(t *testing.T) {
	t.Parallel()

	found := faults(observed(planned()), observed(planned(), started("unit0001"), progressed(), started("phase001"), ended("phase001")))
	if len(found) != 1 || !strings.Contains(found[0], "never ended") {
		t.Fatalf("the tier found %v against a run that left a scope open, want it failed for a scope it never ended", found)
	}
}

func TestARunThatEndsEveryScopeItStartsPasses(t *testing.T) {
	t.Parallel()

	applied := observed(planned(), started("unit0001"), started("phase001"), progressed(), ended("phase001"), ended("unit0001"))
	if found := faults(observed(planned()), applied); len(found) != 0 {
		t.Fatalf("the tier found %v against a run that ended every scope it started", found)
	}
}
