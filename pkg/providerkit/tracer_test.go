package providerkit

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEventTracerDeclareStagesSendsAStagePlanEvent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	unit := UnitStage(naming.UnitEnvironment, "Environment")
	phase := PhaseStage(unit.Name, progressv1.Phase_PHASE_PROVISIONING)
	DeclareStages(tracer, unit)
	DeclareStages(tracer, phase)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	first := events[0].GetStagePlan()
	if len(first.GetStages()) != 1 || first.GetStages()[0].GetTitle() != "Environment" {
		t.Fatalf("first StagePlanEvent stages = %+v", first.GetStages())
	}
	if len(first.GetStages()[0].GetParentId()) != 0 {
		t.Errorf("unit ParentId = %x, want empty (a unit is a root)", first.GetStages()[0].GetParentId())
	}
	if got := first.GetStages()[0].GetPhase(); got != progressv1.Phase_PHASE_UNSPECIFIED {
		t.Errorf("unit Phase = %v, want PHASE_UNSPECIFIED", got)
	}

	second := events[1].GetStagePlan()
	if string(second.GetStages()[0].GetParentId()) != string(first.GetStages()[0].GetId()) {
		t.Error("phase stage's ParentId does not match the declared unit's Id")
	}
	if got := second.GetStages()[0].GetPhase(); got != progressv1.Phase_PHASE_PROVISIONING {
		t.Errorf("phase stage Phase = %v, want PHASE_PROVISIONING", got)
	}
}

func TestDeclaredUnitAndPhaseIDsAreTheSharedNamingDigests(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	unit := UnitStage(naming.UnitEnvironment, "Environment")
	DeclareStages(tracer,
		unit,
		PhaseStage(unit.Name, progressv1.Phase_PHASE_BUILDING),
		PhaseStage(unit.Name, progressv1.Phase_PHASE_PROVISIONING),
	)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	stages := stream.recorded()[0].GetStagePlan().GetStages()
	for i, want := range []string{"9f2ecbbdfa2db89d", "4b5ac07b8124802c", "ed0ca2aae3a67905"} {
		if got := hex.EncodeToString(stages[i].GetId()); got != want {
			t.Errorf("stage %d id = %s, want the naming digest %s", i, got, want)
		}
		if len(stages[i].GetId()) != naming.StageIDLen {
			t.Errorf("stage %d id is %d bytes, want %d", i, len(stages[i].GetId()), naming.StageIDLen)
		}
	}
}

func TestDetailStagesMintTheirOwnIDUnderTheirPhase(t *testing.T) {
	t.Parallel()

	unit := UnitStage(naming.UnitPromotion, "Promotion")
	phase := PhaseStage(unit.Name, progressv1.Phase_PHASE_FINALIZING)
	first := NewStage(phase, "detail")
	second := NewStage(phase, "detail")

	if first.ID == second.ID {
		t.Error("two detail stages share an id, want each minted on its own")
	}
	if first.ParentID != phase.ID {
		t.Error("a detail stage hangs off something other than its phase")
	}
}

func TestDeclareStagesToleratesNoTracer(t *testing.T) {
	t.Parallel()

	DeclareStages(nil, UnitStage(naming.UnitEnvironment, "Environment"))
}

func TestEventTracerSpanUsesTheStageIDAsTheSpanID(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := UnitStage(naming.UnitEnvironment, "Environment")
	child := NewStage(root, "web")
	start := time.Unix(1000, 0)
	end := time.Unix(1005, 0)
	tracer.Span(child.ID, child.ParentID, child.Title, start, end, nil, AttrApp("web"), AttrResourceCount(3))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	span := stream.recorded()[0].GetSpan()
	if string(span.GetSpanId()) != string(child.ID[:]) {
		t.Error("SpanEvent.SpanId does not match the stage's id")
	}
	if string(span.GetParentSpanId()) != string(root.ID[:]) {
		t.Error("SpanEvent.ParentSpanId does not match the parent stage's id")
	}
	if span.GetName() != "web" {
		t.Errorf("SpanEvent.Name = %q, want %q", span.GetName(), "web")
	}
	if span.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("SpanEvent.Status = %v, want OK", span.GetStatus())
	}
	if span.GetStartTimeUnixNano() != start.UnixNano() || span.GetEndTimeUnixNano() != end.UnixNano() {
		t.Errorf("SpanEvent times = %d/%d, want %d/%d", span.GetStartTimeUnixNano(), span.GetEndTimeUnixNano(), start.UnixNano(), end.UnixNano())
	}
	if got := attributeValue(span.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_APP); got != "web" {
		t.Errorf("APP attribute = %q, want the kit's string key mapped onto the wire enum", got)
	}
	if got := attributeValue(span.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT); got != "3" {
		t.Errorf("RESOURCE_COUNT attribute = %q", got)
	}
}

func TestEventTracerSpanRecordsAFailureAsAnErrorKindNeverRawText(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	secret := "postgres://user:hunter2@10.0.0.1:5432/db AKIAABCDEF1234567890"
	stage := UnitStage(naming.UnitEnvironment, "Environment")
	tracer.Span(stage.ID, stage.ParentID, stage.Title, time.Now(), time.Now(), errors.New(secret))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	span := stream.recorded()[0].GetSpan()
	if span.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("SpanEvent.Status = %v, want ERROR", span.GetStatus())
	}
	got := attributeValue(span.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND)
	if got == "" {
		t.Fatal("no ATTRIBUTE_KEY_ERROR_KIND attribute on a failed span")
	}
	if strings.Contains(got, "hunter2") {
		t.Fatal("ERROR_KIND attribute carried the raw error text")
	}
	if got != ErrorKindFailed {
		t.Errorf("ERROR_KIND = %q, want a bounded classification", got)
	}
}

func TestClassifyError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, ErrorKindCanceled},
		{"timeout", context.DeadlineExceeded, ErrorKindTimeout},
		{"anything else", errors.New("boom"), ErrorKindFailed},
	} {
		if got := ClassifyError(tc.err); got != tc.want {
			t.Errorf("ClassifyError(%v) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStageTitlesAreSanitized(t *testing.T) {
	t.Parallel()

	if got := UnitStage(naming.UnitEnvironment, "\x1b[2J").Title; got != "[2J" {
		t.Errorf("UnitStage() title = %q, want the control characters gone", got)
	}
	if got := UnitStage(naming.UnitEnvironment, "   ").Title; got != "stage" {
		t.Errorf("UnitStage() title = %q, want a fallback title", got)
	}
	if got := UnitStage(naming.UnitEnvironment, strings.Repeat("a", maxStageTitleLen*2)).Title; len(got) > maxStageTitleLen {
		t.Errorf("UnitStage() title is %d long, want it capped at %d", len(got), maxStageTitleLen)
	}
}

func attributeValue(attrs []*progressv1.SpanAttribute, key progressv1.AttributeKey) string {
	for _, a := range attrs {
		if a.GetKey() == key {
			return a.GetValue()
		}
	}
	return ""
}
