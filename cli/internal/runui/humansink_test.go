package runui

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAHoldPausesTheLiveRegionAndTheResumeRedrawsIt(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	s := newHumanSink(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight})
	var sink events.Sink = s
	t.Cleanup(func() { _ = sink.Close() })

	unit, phase := appStage(1), appStage(2)
	sink.Receive(startedEvent(unit, nil, progressv1.Phase_PHASE_UNSPECIFIED, "app-a"))
	sink.Receive(startedEvent(phase, unit, progressv1.Phase_PHASE_DEPLOY, "Uploading"))
	sink.Receive(progressEvent(phase, "uploading assets", 1, u32(10)))

	sink.Receive(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{Url: "https://ocel.dev/vars"}}})
	if s.r.liveLines != 0 {
		t.Errorf("liveLines = %d after the hold, want the live region taken back while the run waits", s.r.liveLines)
	}

	out.Reset()
	sink.Receive(progressEvent(phase, "uploading assets", 2, u32(10)))
	if strings.Contains(out.String(), "app-a") {
		t.Errorf("wrote %q while held, want no live region drawn over the interaction", out.String())
	}

	sink.Receive(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{Resumed: &streamv1.ResumedEvent{Reason: "the page was answered"}}})
	if !strings.Contains(out.String(), "app-a") {
		t.Errorf("after the resume, out = %q, want the live region drawn again", out.String())
	}
}

func TestAPromptsHoldDrawsNothingOfItsOwnAroundTheQuestion(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	sink := newHumanSink(&out, Presentation{Format: FormatHuman, Width: defaultWidth, Height: defaultHeight})
	t.Cleanup(func() { _ = sink.Close() })

	sink.Receive(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{}}})
	sink.Receive(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{Resumed: &streamv1.ResumedEvent{Reason: "answered"}}})

	if out.String() != "" {
		t.Errorf("out = %q, want a prompt's hold and resume silent: the question it holds the terminal for is the only thing drawn", out.String())
	}
}

func TestAHumanSinkOwnsTheTerminalOnlyOnceARunDrawsOnIt(t *testing.T) {
	owners := liveOwners.Load()
	live := Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight}

	var idle safeBuffer
	unused := NewHumanSink(&idle, live)
	if liveOwners.Load() != owners {
		t.Errorf("owners = %d with an idle human sink, want %d: the terminal stays free for a command that never begins a run", liveOwners.Load(), owners)
	}
	if err := unused.Close(); err != nil {
		t.Fatal(err)
	}
	if idle.String() != "" {
		t.Errorf("an idle human sink wrote %q, want nothing from a sink no run drew on", idle.String())
	}

	var out safeBuffer
	used := NewHumanSink(&out, live)
	used.Receive(startedEvent(appStage(1), nil, progressv1.Phase_PHASE_UNSPECIFIED, "app-a"))
	if liveOwners.Load() != owners+1 {
		t.Errorf("owners = %d after a run drew on the sink, want %d", liveOwners.Load(), owners+1)
	}
	if err := used.Close(); err != nil {
		t.Fatal(err)
	}
	if liveOwners.Load() != owners {
		t.Errorf("owners = %d after the sink closed, want %d", liveOwners.Load(), owners)
	}
}
