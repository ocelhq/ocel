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
