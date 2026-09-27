package events_test

import (
	"fmt"
	"strings"
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestOutputSplitsWritesIntoLinesAndKeepsOnlyTheLastCarriageReturnRewrite(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	out := build.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT)

	fmt.Fprint(out, "compiling\nprogress 10%\rprogress 50%")
	fmt.Fprint(out, "\rprogress 100%\n\ndone")
	build.End(nil)

	var lines []string
	for _, ev := range sink.received() {
		if ev.GetOutput() == nil {
			continue
		}
		if ev.GetOutput().GetStream() != progressv1.Stream_STREAM_STDOUT || ev.GetLevel() != progressv1.Level_LEVEL_INFO ||
			ev.GetPhase() != progressv1.Phase_PHASE_BUILD {
			t.Fatalf("line %q = stream %s level %s phase %s, want stdout INFO in build",
				ev.GetMessage(), ev.GetOutput().GetStream(), ev.GetLevel(), ev.GetPhase())
		}
		lines = append(lines, ev.GetMessage())
	}
	want := []string{"compiling", "progress 100%", "done"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if last := sink.received()[len(sink.received())-1]; last.GetEnded() == nil {
		t.Fatal("the scope ended before its last line")
	}
}

func TestOutputEmitsALineThatNeverEndsOnceItPassesSixtyFourKiB(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	out := run.Phase(progressv1.Phase_PHASE_BUILD).Output(progressv1.Level_LEVEL_DEBUG, progressv1.Stream_STREAM_STDERR)

	fmt.Fprint(out, strings.Repeat("x", 64<<10+1))

	got := sink.received()
	if last := got[len(got)-1]; last.GetOutput() == nil || len(last.GetMessage()) != 64<<10+1 {
		t.Fatalf("last event = %d bytes of output %v, want the whole unterminated line", len(last.GetMessage()), last.GetOutput() != nil)
	}
}
