package providerkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestEventTracerDeclareStagesSendsAStagePlanEvent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := NewRootStage("Provisioning")
	child := NewStage(root, "web")
	DeclareStages(tracer, false, root)
	DeclareStages(tracer, true, child)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	first := events[0].GetStagePlan()
	if first.GetFinal() {
		t.Error("first StagePlanEvent.Final = true, want false")
	}
	if len(first.GetStages()) != 1 || first.GetStages()[0].GetTitle() != "Provisioning" {
		t.Fatalf("first StagePlanEvent stages = %+v", first.GetStages())
	}
	if len(first.GetStages()[0].GetParentId()) != 0 {
		t.Errorf("root stage ParentId = %x, want empty (no parent)", first.GetStages()[0].GetParentId())
	}

	second := events[1].GetStagePlan()
	if !second.GetFinal() {
		t.Error("second StagePlanEvent.Final = false, want true")
	}
	if string(second.GetStages()[0].GetParentId()) != string(first.GetStages()[0].GetId()) {
		t.Error("child stage's ParentId does not match the declared parent stage's Id")
	}
}

func TestDeclareStagesToleratesNoTracer(t *testing.T) {
	t.Parallel()

	DeclareStages(nil, true, NewRootStage("Provisioning"))
}

func TestEventTracerSpanUsesTheStageIDAsTheSpanID(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := NewRootStage("Provisioning")
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
	stage := NewRootStage("Provisioning")
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

	if got := NewRootStage("\x1b[2J").Title; got != "[2J" {
		t.Errorf("NewRootStage() title = %q, want the control characters gone", got)
	}
	if got := NewRootStage("   ").Title; got != "stage" {
		t.Errorf("NewRootStage() title = %q, want a fallback title", got)
	}
	if got := NewRootStage(strings.Repeat("a", maxStageTitleLen*2)).Title; len(got) > maxStageTitleLen {
		t.Errorf("NewRootStage() title is %d long, want it capped at %d", len(got), maxStageTitleLen)
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
