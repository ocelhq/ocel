package tracing

import (
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestAStepsSpanIDIsTheSameOnBothSidesOfOneCallAndDiffersAcrossCalls(t *testing.T) {
	step := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	first, second := trace.SpanID{0xa}, trace.SpanID{0xb}

	once, again := StepSpanID(first, step), StepSpanID(first, step)
	if once != again {
		t.Error("StepSpanID() differs for the same call and step")
	}
	if StepSpanID(first, step) == StepSpanID(second, step) {
		t.Error("StepSpanID() is the same for one step in two calls")
	}
	if StepSpanID(first, step) == StepSpanID(first, []byte{8, 7, 6, 5, 4, 3, 2, 1}) {
		t.Error("StepSpanID() is the same for two steps in one call")
	}
	if !once.IsValid() {
		t.Error("StepSpanID() is the zero span ID")
	}
}
