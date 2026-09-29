package run_test

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
		if ev.GetOperation().GetOutput() == nil {
			continue
		}
		if ev.GetOperation().GetOutput().GetStream() != progressv1.Stream_STREAM_STDOUT || ev.GetOperation().GetLevel() != progressv1.Level_LEVEL_INFO ||
			ev.GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD {
			t.Fatalf("line %q = stream %s level %s phase %s, want stdout INFO in build",
				ev.GetOperation().GetMessage(), ev.GetOperation().GetOutput().GetStream(), ev.GetOperation().GetLevel(), ev.GetOperation().GetPhase())
		}
		lines = append(lines, ev.GetOperation().GetMessage())
	}
	want := []string{"compiling", "progress 100%", "done"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if last := sink.received()[len(sink.received())-1]; last.GetOperation().GetEnded() == nil {
		t.Fatal("the span ended before its last line")
	}
}

func TestOutputEmitsALineThatNeverEndsOnceItPassesSixtyFourKiB(t *testing.T) {
	sink := &recording{}
	run, _ := begin(t, sink)
	out := run.Phase(progressv1.Phase_PHASE_BUILD).Output(progressv1.Level_LEVEL_DEBUG, progressv1.Stream_STREAM_STDERR)

	fmt.Fprint(out, strings.Repeat("x", 64<<10+1))

	got := sink.received()
	if last := got[len(got)-1]; last.GetOperation().GetOutput() == nil || len(last.GetOperation().GetMessage()) != 64<<10+1 {
		t.Fatalf("last event = %d bytes of output %v, want the whole unterminated line", len(last.GetOperation().GetMessage()), last.GetOperation().GetOutput() != nil)
	}
}
