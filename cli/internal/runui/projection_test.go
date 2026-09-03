package runui

import (
	"slices"
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

func TestTheProviderResultIsNotProjectedAtTheHuman(t *testing.T) {
	t.Parallel()

	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	got := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
		Success:     true,
		PromotionId: "prm_1",
		Apps:        []*progressv1.AppResult{{App: "web", Urls: []string{"https://app.example.com"}}},
		Functions:   []*progressv1.FunctionOutput{{LogicalName: "server", Url: "https://fn.example.com"}},
	}}}))

	if len(got) != 0 {
		t.Errorf("the provider's result was projected as %q, want the run's own result to be the only one a human is shown", got)
	}
}

func TestTheSuccessResultIsTheURLsColoured(t *testing.T) {
	t.Parallel()

	p := newProjector(Presentation{Format: FormatHuman, TTY: true, Color: true, Width: defaultWidth})
	got := strings.Join(p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
		Success:    true,
		Headline:   "Deployed shop to production",
		DurationMs: 1000,
		Apps:       []*progressv1.AppResult{{App: "web", Urls: []string{"https://shop.example"}}},
		LogPath:    "run.log",
	}}}), "\n")

	for _, want := range []string{
		"\x1b[32;1m✓ Deployed shop to production in 1s\x1b[0;22m",
		blockIndent + "\x1b[36mhttps://shop.example\x1b[0m",
		"\x1b[2m" + blockIndent + "Details: run.log\x1b[22m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("projection =\n%q\nwant it to carry %q", got, want)
		}
	}
}

func projectedResult(t *testing.T, apps ...*progressv1.AppResult) []string {
	t.Helper()
	p := newProjector(Presentation{Format: FormatHuman, TTY: true, Color: true, Width: defaultWidth})
	return p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
		Success:  true,
		Headline: "Deployed",
		Apps:     apps,
	}}})
}

func TestTheSuccessResultLabelsEveryAppOnceTheProjectCarriesMoreThanOne(t *testing.T) {
	t.Parallel()

	got := projectedResult(t,
		&progressv1.AppResult{App: "web", Urls: []string{"https://shop.example"}},
		&progressv1.AppResult{App: "admin", Urls: []string{"https://admin.shop.example"}},
	)

	want := []string{
		blockIndent + "web  " + "  " + "\x1b[36mhttps://shop.example\x1b[0m",
		blockIndent + "admin" + "  " + "\x1b[36mhttps://admin.shop.example\x1b[0m",
	}
	if !slices.Contains(got, want[0]) || !slices.Contains(got, want[1]) {
		t.Errorf("projection =\n%q\nwant it to carry %q", got, want)
	}
}

func TestTheSuccessResultIndentsAnAppsSecondURLUnderTheFirst(t *testing.T) {
	t.Parallel()

	got := projectedResult(t,
		&progressv1.AppResult{App: "web", Urls: []string{"https://shop.example", "https://www.shop.example"}},
		&progressv1.AppResult{App: "admin", Urls: []string{"https://admin.shop.example"}},
	)

	want := blockIndent + "     " + "  " + "\x1b[36mhttps://www.shop.example\x1b[0m"
	if !slices.Contains(got, want) {
		t.Errorf("projection =\n%q\nwant it to carry %q", got, want)
	}
}

func TestTheSuccessResultSaysSoWhenAnAppAnswersNowhere(t *testing.T) {
	t.Parallel()

	got := projectedResult(t,
		&progressv1.AppResult{App: "web", Urls: []string{"https://shop.example"}},
		&progressv1.AppResult{App: "admin"},
	)

	want := blockIndent + "admin" + "  " + "\x1b[2mno public url\x1b[22m"
	if !slices.Contains(got, want) {
		t.Errorf("projection =\n%q\nwant it to carry %q", got, want)
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
	if len(start) != 0 {
		t.Fatalf("on the phase being declared, committed %q, want nothing until it says something", start)
	}

	var got []string
	for _, message := range []string{"step 1", "step 2"} {
		got = append(got, p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
			Progress: &progressv1.ProgressEvent{StageId: phase, Message: message},
		}}))...)
	}
	if want := []string{"→ web › Building"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("committed %q mid-phase, want %q and the block buffered until the phase completes", got, want)
	}

	got = p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
		SpanId:            phase,
		StartTimeUnixNano: 1,
		EndTimeUnixNano:   int64(6*time.Second) + 1,
	}}}))
	want := []string{"", okMark + " web  6s", "  step 1", "  step 2"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestABlockDropsBlankLinesAtTheEdgesOfWhatItIsGivenAndKeepsTheOnesInside(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth})

	p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{
		Stages: []*progressv1.Stage{
			{Id: unit, Title: "web"},
			{Id: phase, ParentId: unit, Title: "Building", Phase: progressv1.Phase_PHASE_BUILDING},
		},
	}}}))
	for _, message := range []string{"", "\n", "  \n\n", "\n\nPackages: +812\n\ncompiled\n\n"} {
		p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
			Log: &progressv1.LogEvent{StageId: phase, Message: message},
		}}))
	}

	got := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
		SpanId:            phase,
		StartTimeUnixNano: 1,
		EndTimeUnixNano:   int64(6*time.Second) + 1,
	}}}))
	want := []string{"", okMark + " web  6s", "  Packages: +812", "  ", "  compiled"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%q\nwant\n%q", got, want)
	}
}

func TestABlockIsHeadedByWhatTheRosterSaysTheUnitRuns(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		roster []*progressv1.Stage
		want   string
	}{
		{
			"a unit that runs one phase is its own headline",
			[]*progressv1.Stage{
				{Id: appStage(1), Title: "Edge"},
				{Id: appStage(2), ParentId: appStage(1), Title: "Provisioning"},
			},
			okMark + " Edge  6s",
		},
		{
			"a unit that runs more than one names the phase, from the first block on",
			[]*progressv1.Stage{
				{Id: appStage(1), Title: "Environment"},
				{Id: appStage(2), ParentId: appStage(1), Title: "Provisioning"},
				{Id: appStage(3), ParentId: appStage(1), Title: "Uploading"},
			},
			okMark + " Environment › Provisioning  6s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
			p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{
				StagePlan: &progressv1.StagePlanEvent{Stages: tc.roster},
			}}))

			got := p.project(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
				SpanId:            appStage(2),
				StartTimeUnixNano: 1,
				EndTimeUnixNano:   int64(6*time.Second) + 1,
			}}}))
			if !slices.Contains(got, tc.want) {
				t.Errorf("the first block closed as %q, want %q", got, tc.want)
			}
		})
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
		{"interrupted", &streamv1.RunResultEvent{Interrupted: true, Headline: "Cancelled"}, warnMark + " web interrupted"},
		{"failed", &streamv1.RunResultEvent{Detail: "boom"}, failMark + " web failed"},
		{"succeeded", &streamv1.RunResultEvent{Success: true}, warnMark + " web unfinished"},
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
			if !strings.Contains(got, tc.want+"\n  step 1\n") {
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
