package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func TestEventTracerDeclareStagesSendsAStagePlanEvent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := deploy.NewRootStage("Provisioning")
	child := deploy.NewStage(root, "web")
	tracer.DeclareStages(false, root)
	tracer.DeclareStages(true, child)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if len(stream.events) != 2 {
		t.Fatalf("got %d events, want 2", len(stream.events))
	}

	first := stream.events[0].GetStagePlan()
	if first == nil {
		t.Fatal("first event is not a StagePlanEvent")
	}
	if first.GetFinal() {
		t.Error("first StagePlanEvent.Final = true, want false")
	}
	if len(first.GetStages()) != 1 || first.GetStages()[0].GetTitle() != "Provisioning" {
		t.Fatalf("first StagePlanEvent stages = %+v", first.GetStages())
	}
	if len(first.GetStages()[0].GetParentId()) != 0 {
		t.Errorf("root stage ParentId = %x, want empty (no parent)", first.GetStages()[0].GetParentId())
	}

	second := stream.events[1].GetStagePlan()
	if second == nil {
		t.Fatal("second event is not a StagePlanEvent")
	}
	if !second.GetFinal() {
		t.Error("second StagePlanEvent.Final = false, want true")
	}
	if len(second.GetStages()) != 1 || second.GetStages()[0].GetTitle() != "web" {
		t.Fatalf("second StagePlanEvent stages = %+v", second.GetStages())
	}
	if string(second.GetStages()[0].GetParentId()) != string(first.GetStages()[0].GetId()) {
		t.Error("child stage's ParentId does not match the declared parent stage's Id")
	}
}

func TestEventTracerSpanUsesTheStageIDAsTheSpanID(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := deploy.NewRootStage("Provisioning")
	child := deploy.NewStage(root, "web")
	start := time.Unix(1000, 0)
	end := time.Unix(1005, 0)
	tracer.Span(child.ID, child.ParentID, child.Title, start, end, nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if len(stream.events) != 1 {
		t.Fatalf("got %d events, want 1", len(stream.events))
	}
	span := stream.events[0].GetSpan()
	if span == nil {
		t.Fatal("event is not a SpanEvent")
	}
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
}

func TestEventTracerSpanRecordsAFailureAsAnErrorKindNeverRawText(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	secret := "postgres://user:hunter2@10.0.0.1:5432/db AKIAABCDEF1234567890 https://presigned.example/put?X-Amz-Signature=deadbeef"
	stage := deploy.NewRootStage("Provisioning")
	tracer.Span(stage.ID, stage.ParentID, stage.Title, time.Now(), time.Now(), errors.New(secret))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	span := stream.events[0].GetSpan()
	if span.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("SpanEvent.Status = %v, want ERROR", span.GetStatus())
	}

	var sawErrorKind bool
	for _, a := range span.GetAttributes() {
		if a.GetKey() == progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND {
			sawErrorKind = true
			if a.GetValue() == secret {
				t.Fatal("ERROR_KIND attribute carried the raw error text")
			}
			if a.GetValue() != deploy.ErrorKindFailed {
				t.Errorf("ERROR_KIND = %q, want a bounded classification", a.GetValue())
			}
		}
	}
	if !sawErrorKind {
		t.Fatal("no ATTRIBUTE_KEY_ERROR_KIND attribute on a failed span")
	}

	if span.GetName() == secret {
		t.Fatal("SpanEvent.Name carried the raw error text")
	}
}

func TestEventTracerSpanCarriesResourceIdentityNeverTheURN(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	stack := deploy.NewRootStage("Provisioning")
	tracer.Span(stack.ID, stack.ParentID, "resource operation failed", time.Now(), time.Now(), errors.New("boom"),
		deploy.AttrResourceType("aws:s3/bucket:Bucket"), deploy.AttrResourceName("my-bucket"))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	span := stream.events[0].GetSpan()

	var sawType, sawName bool
	for _, a := range span.GetAttributes() {
		switch a.GetKey() {
		case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
			sawType = true
			if a.GetValue() != "aws:s3/bucket:Bucket" {
				t.Errorf("RESOURCE_TYPE = %q, want the type token", a.GetValue())
			}
		case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
			sawName = true
			if a.GetValue() != "my-bucket" {
				t.Errorf("RESOURCE_NAME = %q, want the logical name", a.GetValue())
			}
		}
	}
	if !sawType || !sawName {
		t.Fatalf("span missing resource identity attrs: %+v", span.GetAttributes())
	}

	serialized := span.String()
	for _, leaked := range []string{"urn:pulumi", "prod", "acme-shop"} {
		if strings.Contains(serialized, leaked) {
			t.Fatalf("SpanEvent leaked URN component %q: %s", leaked, serialized)
		}
	}
}

func TestEventTracerRootStageHasNoParentID(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	tracer := newEventTracer(sender)

	root := deploy.NewRootStage("Preparing")
	tracer.Span(root.ID, root.ParentID, root.Title, time.Now(), time.Now(), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	span := stream.events[0].GetSpan()
	if len(span.GetParentSpanId()) != 0 {
		t.Errorf("root span ParentSpanId = %x, want empty", span.GetParentSpanId())
	}
}
