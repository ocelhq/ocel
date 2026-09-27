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
	got := p.project(lift(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
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
	got := strings.Join(p.project(&streamv1.RunEvent{Body: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
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
			t.Errorf("projection =\n%q\nwant it to contain %q", got, want)
		}
	}
}

func projectedResult(t *testing.T, apps ...*progressv1.AppResult) []string {
	t.Helper()
	p := newProjector(Presentation{Format: FormatHuman, TTY: true, Color: true, Width: defaultWidth})
	return p.project(&streamv1.RunEvent{Body: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
		Success:  true,
		Headline: "Deployed",
		Apps:     apps,
	}}})
}

func TestTheSuccessResultLabelsEveryAppOnceTheProjectHasMoreThanOne(t *testing.T) {
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
		t.Errorf("projection =\n%q\nwant it to contain %q", got, want)
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
		t.Errorf("projection =\n%q\nwant it to contain %q", got, want)
	}
}

func TestTheSuccessResultPrintsTheNoteBesideTheURLsItPrinted(t *testing.T) {
	t.Parallel()

	notes := []string{
		"www.shop.example is not served yet: its DNS record is not written yet",
		"api.shop.example is not served yet: it does not answer",
	}
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	got := p.project(&streamv1.RunEvent{Body: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
		Success:  true,
		Headline: "Deployed",
		Apps:     []*progressv1.AppResult{{App: "web", Urls: []string{"https://shop.example"}}},
		UrlNotes: notes,
	}}})

	if !slices.Contains(got, blockIndent+"https://shop.example") ||
		!slices.Contains(got, blockIndent+"www.shop.example is not served yet: its DNS record is not written yet") ||
		!slices.Contains(got, blockIndent+"api.shop.example is not served yet: it does not answer") {
		t.Errorf("projection =\n%q\nwant both the url that serves and the note on the hostname that does not yet", got)
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
		t.Errorf("projection =\n%q\nwant it to contain %q", got, want)
	}
}

func TestEveryPhaseCommitsAStartLineThenItsBlockWhole(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})

	start := projectStarted(p, []scope{
		{id: unit, title: "web"},
		{id: phase, parent: unit, title: "Building", phase: progressv1.Phase_PHASE_BUILD},
	}...)
	if len(start) != 0 {
		t.Fatalf("on the phase being declared, committed %q, want nothing until it says something", start)
	}

	var got []string
	for _, message := range []string{"step 1", "step 2"} {
		got = append(got, p.project(progressEvent(phase, message, 0, nil))...)
	}
	if want := []string{"→ web › Building"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("committed %q mid-phase, want %q and the block buffered until the phase completes", got, want)
	}

	got = p.project(endedEvent(phase, false, 6*time.Second))
	want := []string{"", okMark + " web  6s", "  step 1", "  step 2"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestABlockDropsBlankLinesAtTheEdgesOfWhatItIsGivenAndKeepsTheOnesInside(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth})

	projectStarted(p, []scope{
		{id: unit, title: "web"},
		{id: phase, parent: unit, title: "Building", phase: progressv1.Phase_PHASE_BUILD},
	}...)
	for _, message := range []string{"", "\n", "  \n\n", "\n\nPackages: +812\n\ncompiled\n\n"} {
		p.project(outputEvent(phase, message))
	}

	got := p.project(endedEvent(phase, false, 6*time.Second))
	want := []string{"", okMark + " web  6s", "  Packages: +812", "  ", "  compiled"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%q\nwant\n%q", got, want)
	}
}

func TestABlockIsHeadedByWhatTheRosterSaysTheUnitRuns(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		roster []scope
		want   string
	}{
		{
			"a unit that runs one phase is its own headline",
			[]scope{
				{id: appStage(1), title: "Edge"},
				{id: appStage(2), parent: appStage(1), title: "Provisioning"},
			},
			okMark + " Edge  6s",
		},
		{
			"a unit that runs more than one names the phase, from the first block on",
			[]scope{
				{id: appStage(1), title: "Environment"},
				{id: appStage(2), parent: appStage(1), title: "Provisioning"},
				{id: appStage(3), parent: appStage(1), title: "Uploading"},
			},
			okMark + " Environment › Provisioning  6s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
			projectStarted(p, tc.roster...)

			got := p.project(endedEvent(appStage(2), false, 6*time.Second))
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

	projectStarted(p, []scope{
		{id: unitA, title: "app-a"},
		{id: phaseA, parent: unitA, title: "Provisioning"},
		{id: unitB, title: "app-b"},
		{id: phaseB, parent: unitB, title: "Provisioning"},
	}...)
	p.project(progressEvent(phaseA, "a detail", 0, nil))
	p.project(progressEvent(phaseB, "b detail", 0, nil))

	second := p.project(endedEvent(phaseB, false, 0))
	first := p.project(endedEvent(phaseA, false, 0))

	if !strings.Contains(strings.Join(second, "\n"), "b detail") {
		t.Errorf("first flush = %q, want app-b's block, the first phase to complete", second)
	}
	if !strings.Contains(strings.Join(first, "\n"), "a detail") {
		t.Errorf("second flush = %q, want app-a's block", first)
	}
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
			projectStarted(p, []scope{
				{id: unit, title: "web"},
				{id: phase, parent: unit, title: "Building", phase: progressv1.Phase_PHASE_BUILD},
			}...)
			p.project(progressEvent(phase, "step 1", 0, nil))

			got := strings.Join(p.project(&streamv1.RunEvent{Body: &streamv1.RunEvent_Result{Result: tc.result}}), "\n")
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
	projectStarted(p, []scope{
		{id: unit, title: "web"},
		{id: phase, parent: unit, title: "Building", phase: progressv1.Phase_PHASE_BUILD},
	}...)
	p.project(endedEvent(phase, false, time.Second))

	got := strings.Join(p.project(&streamv1.RunEvent{Body: &streamv1.RunEvent_Result{
		Result: &streamv1.RunResultEvent{Success: true, Headline: "Deployed", DurationMs: 1000},
	}}), "\n")
	if strings.Contains(got, "interrupted") {
		t.Errorf("result projection =\n%s\nwant no interrupted marker on a run nobody interrupted", got)
	}
}

func TestADebugLineIsHiddenFromTheHumanUnlessVerboseEvenInAFailedBlock(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		verbose bool
		want    []string
	}{
		{"quiet", false, []string{"", failMark + " web failed  1s", "  the stack refused the change"}},
		{"verbose", true, []string{"", failMark + " web failed  1s", "  +  aws:s3:Bucket assets creating (0s)", "  the stack refused the change"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			unit, phase := appStage(1), appStage(2)
			p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth, Verbose: tc.verbose})
			projectStarted(p, []scope{
				{id: unit, title: "web"},
				{id: phase, parent: unit, title: "Provisioning", phase: progressv1.Phase_PHASE_DEPLOY},
			}...)
			debug := outputEvent(phase, "+  aws:s3:Bucket assets creating (0s)")
			debug.Level = progressv1.Level_LEVEL_DEBUG
			p.project(debug)
			p.project(outputEvent(phase, "the stack refused the change"))

			got := p.project(endedEvent(phase, true, time.Second))
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("failed block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
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

func projectStarted(p *projector, scopes ...scope) []string {
	var out []string
	for _, sc := range scopes {
		out = append(out, p.project(startedEvent(sc.id, sc.parent, sc.phase, sc.title))...)
	}
	return out
}

func startedEvent(id, parent []byte, phase progressv1.Phase, title string) *streamv1.RunEvent {
	return lift(&progressv1.OperationEvent{Phase: phase, SpanId: id, Message: title, Body: &progressv1.OperationEvent_Started{
		Started: &progressv1.Started{ParentSpanId: parent},
	}})
}

func endedEvent(id []byte, failed bool, d time.Duration) *streamv1.RunEvent {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if failed {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
	}
	return lift(&progressv1.OperationEvent{TimeUnixNano: int64(d) + 1, SpanId: id, Body: &progressv1.OperationEvent_Ended{
		Ended: &progressv1.Ended{Status: status, StartTimeUnixNano: 1},
	}})
}

func TestAStartedAndEndedPhaseCommitsItsStartLineThenItsClosedBlock(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})

	var got []string
	got = append(got, p.project(startedEvent(unit, nil, progressv1.Phase_PHASE_UNSPECIFIED, "web"))...)
	got = append(got, p.project(startedEvent(phase, unit, progressv1.Phase_PHASE_BUILD, ""))...)
	got = append(got, p.project(endedEvent(phase, false, 6*time.Second))...)
	got = append(got, p.project(endedEvent(unit, false, 6*time.Second))...)

	want := []string{"→ web › Building", "", okMark + " web  6s"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("projected =\n%q\nwant\n%q", got, want)
	}
}

func outputEvent(id []byte, line string) *streamv1.RunEvent {
	return lift(&progressv1.OperationEvent{SpanId: id, Message: line, Body: &progressv1.OperationEvent_Output{
		Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDOUT},
	}})
}

func TestOutputLinesAreThatBlocksRawLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		verbose bool
		failed  bool
		want    []string
	}{
		{"hidden from a successful block", false, false, []string{"", okMark + " web  6s"}},
		{"shown when verbose", true, false, []string{"", okMark + " web  6s", "  Packages: +812"}},
		{"shown when the block failed", false, true, []string{"", failMark + " web failed  6s", "  Packages: +812"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			unit, phase := appStage(1), appStage(2)
			p := newProjector(Presentation{Format: FormatHuman, Verbose: tc.verbose, Width: defaultWidth})
			p.project(startedEvent(unit, nil, progressv1.Phase_PHASE_UNSPECIFIED, "web"))
			p.project(startedEvent(phase, unit, progressv1.Phase_PHASE_BUILD, ""))

			if got := p.project(outputEvent(phase, "Packages: +812")); strings.Join(got, "\n") != "→ web › Building" {
				t.Fatalf("on its first output line the phase committed %q, want only its start line", got)
			}
			got := p.project(endedEvent(phase, tc.failed, 6*time.Second))
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("flushed block =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestAScopedMessageIsAProgressLineOfItsBlock(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	p.project(startedEvent(unit, nil, progressv1.Phase_PHASE_UNSPECIFIED, "web"))
	p.project(startedEvent(phase, unit, progressv1.Phase_PHASE_BUILD, ""))

	said := p.project(&streamv1.RunEvent{Level: progressv1.Level_LEVEL_INFO, SpanId: phase, Message: "Generating static pages"})
	if strings.Join(said, "\n") != "→ web › Building" {
		t.Fatalf("on the phase saying something it committed %q, want only its start line", said)
	}
	got := p.project(endedEvent(phase, false, 6*time.Second))
	want := []string{"", okMark + " web  6s", "  Generating static pages"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%q\nwant\n%q", got, want)
	}
}

func counterEvent(id []byte, message string, current uint32, total *uint32) *streamv1.RunEvent {
	return lift(&progressv1.OperationEvent{SpanId: id, Message: message, Body: &progressv1.OperationEvent_Counter{
		Counter: &progressv1.Counter{Current: current, Total: total},
	}})
}

func TestACounterRendersAsCurrentOfTotal(t *testing.T) {
	t.Parallel()

	unit, phase := appStage(1), appStage(2)
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	p.project(startedEvent(unit, nil, progressv1.Phase_PHASE_UNSPECIFIED, "web"))
	p.project(startedEvent(phase, unit, progressv1.Phase_PHASE_BUILD, ""))

	p.project(counterEvent(phase, "Generating static pages", 28, u32(28)))
	got := p.project(endedEvent(phase, false, 6*time.Second))
	want := []string{"", okMark + " web  6s", "  Generating static pages (28/28)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("flushed block =\n%q\nwant\n%q", got, want)
	}
}
