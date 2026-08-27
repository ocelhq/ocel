package runui

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAnArmWithNoOverrideIsProjectedFromItsDescriptor(t *testing.T) {
	t.Parallel()

	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	got := p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Resumed{Resumed: &streamv1.ResumedEvent{
		Reason: "the page was answered",
	}}})

	want := []string{
		"Resumed",
		"  reason: the page was answered",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("projection =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEveryPhaseCommitsAStartLineThenItsBlockWhole(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})

	start := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
		Stages: []*progressv1.Stage{
			{Id: unit, Title: "web"},
			{Id: phase, ParentId: unit, Title: "Building", Phase: progressv1.Phase_PHASE_BUILDING},
		},
	}}}))
	if want := []string{"→ web › Building"}; strings.Join(start, "\n") != strings.Join(want, "\n") {
		t.Fatalf("on the phase beginning, committed %q, want %q", start, want)
	}

	var got []string
	for _, message := range []string{"step 1", "step 2"} {
		got = append(got, p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
			Progress: &progressv1.ProgressEvent{StageId: phase, Message: message},
		}}))...)
	}
	if len(got) != 0 {
		t.Fatalf("committed %q mid-phase, want the block buffered until the phase completes", got)
	}

	got = p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
		SpanId:            phase,
		StartTimeUnixNano: 1,
		EndTimeUnixNano:   int64(6*time.Second) + 1,
	}}}))
	want := []string{"  step 1", "  step 2", okMark + " web › Building  6s"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBlocksFlushInPhaseCompletionOrder(t *testing.T) {
	t.Parallel()

	unitA, phaseA := appStage(1), appStage(2)
	unitB, phaseB := appStage(3), appStage(4)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})

	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
		Stages: []*progressv1.Stage{
			{Id: unitA, Title: "app-a"},
			{Id: phaseA, ParentId: unitA, Title: "Provisioning"},
			{Id: unitB, Title: "app-b"},
			{Id: phaseB, ParentId: unitB, Title: "Provisioning"},
		},
	}}}))
	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{StageId: phaseA, Message: "a detail"},
	}}))
	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{StageId: phaseB, Message: "b detail"},
	}}))

	second := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{SpanId: phaseB}}}))
	first := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{SpanId: phaseA}}}))

	if !strings.Contains(strings.Join(second, "\n"), "b detail") {
		t.Errorf("first flush = %q, want app-b's block, the first phase to complete", second)
	}
	if !strings.Contains(strings.Join(first, "\n"), "a detail") {
		t.Errorf("second flush = %q, want app-a's block", first)
	}
}

func operation(ev *progressv1.OperationEvent) *streamv1.RunEvent {
	return &streamv1.RunEvent{Event: &streamv1.RunEvent_Operation{Operation: ev}}
}

func TestAnOpenBlockFlushesWithTheOutcomeTheRunActuallyHad(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		result *streamv1.RunResultEvent
		want   string
	}{
		{"interrupted", &streamv1.RunResultEvent{Interrupted: true, Headline: "Cancelled"}, warnMark + " web › Building interrupted"},
		{"failed", &streamv1.RunResultEvent{Detail: "boom"}, failMark + " web › Building failed"},
		{"succeeded", &streamv1.RunResultEvent{Success: true}, warnMark + " web › Building unfinished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			unit, phase := appStage(1), appStage(2)
			p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
			p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
				Stages: []*progressv1.Stage{
					{Id: unit, Title: "web"},
					{Id: phase, ParentId: unit, Title: "Building", Phase: progressv1.Phase_PHASE_BUILDING},
				},
			}}}))
			p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
				Progress: &progressv1.ProgressEvent{StageId: phase, Message: "step 1"},
			}}))

			got := strings.Join(p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{Result: tc.result}}), "\n")
			if !strings.Contains(got, "  step 1\n"+tc.want+"\n") {
				t.Errorf("result projection =\n%s\nwant the in-flight block flushed whole, closed by %q", got, tc.want)
			}
		})
	}
}

func TestARunWhoseEveryPhaseCompletedSaysNothingAboutBeingInterrupted(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
		Stages: []*progressv1.Stage{
			{Id: unit, Title: "web"},
			{Id: phase, ParentId: unit, Title: "Building", Phase: progressv1.Phase_PHASE_BUILDING},
		},
	}}}))
	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
		SpanId: phase, StartTimeUnixNano: 1, EndTimeUnixNano: int64(time.Second) + 1,
	}}}))

	got := strings.Join(p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{
		Result: &streamv1.RunResultEvent{Success: true, Headline: "Deployed", DurationMs: 1000},
	}}), "\n")
	if strings.Contains(got, "interrupted") {
		t.Errorf("result projection =\n%s\nwant no interrupted marker on a run nobody interrupted", got)
	}
}

func TestADegradationKeepsItsMarkAndItsOneLine(t *testing.T) {
	t.Parallel()

	got := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth}).project(
		operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Degraded{Degraded: &progressv1.DegradedEvent{
			Need:   "edge-middleware",
			Detail: "web: middleware runs in the origin",
		}}}))

	want := []string{warnMark + " edge-middleware: web: middleware runs in the origin"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("projection = %q, want %q", got, want)
	}
}

func TestAKindTheProjectionHasNeverSeenStillRenders(t *testing.T) {
	t.Parallel()

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("cli/stream/v1/future.proto"),
		Package: proto.String("cli.stream.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("FutureQuotaEvent"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name:   proto.String("account"),
					Number: proto.Int32(1),
					Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				},
				{
					Name:   proto.String("remaining"),
					Number: proto.Int32(2),
					Type:   descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
					Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				},
			},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("building the hypothetical envelope kind: %v", err)
	}
	md := file.Messages().Get(0)
	m := dynamicpb.NewMessage(md)
	m.Set(md.Fields().ByName("account"), protoreflect.ValueOfString("acct-42"))
	m.Set(md.Fields().ByName("remaining"), protoreflect.ValueOfInt64(7))

	got := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth}).render(m)

	want := []string{"Future quota", "  account: acct-42", "  remaining: 7"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("projection of a kind added after this code shipped =\n%s\nwant\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
