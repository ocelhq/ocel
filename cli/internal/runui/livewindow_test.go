package runui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func spineOf(t *testing.T, s *Stream, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		unit, phase := appStage(byte(2*i)), appStage(byte(2*i+1))
		s.Emit(stagePlanEvent(
			&progressv1.Stage{Id: unit, Title: fmt.Sprintf("app-%02d", i)},
			&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
		))
		s.Emit(progressEvent(phase, "compiling", 6, u32(9)))
	}
}

func TestOnlyTheProjectionWithoutAWindowCommitsPhaseStartLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		present Presentation
		want    bool
	}{
		{"a live terminal", Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight}, false},
		{"a pipe", Presentation{Format: FormatHuman, Width: defaultWidth}, true},
		{"a terminal with --verbose", Presentation{Format: FormatHuman, TTY: true, Verbose: true, Width: defaultWidth, Height: defaultHeight}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out safeBuffer
			s := NewStream(&out, tc.present)
			t.Cleanup(func() { _ = s.Close() })

			unit, phase := appStage(1), appStage(2)
			s.Emit(stagePlanEvent(
				&progressv1.Stage{Id: unit, Title: "web"},
				&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
			))
			s.Emit(progressEvent(phase, "compiling", 1, u32(2)))

			if got := strings.Contains(out.String(), startMark+" web › Building\n"); got != tc.want {
				t.Errorf("committed the phase-start line = %v, want %v — the window is the liveness surface when there is one", got, tc.want)
			}
		})
	}
}

func TestAUnitIsOneRowCarryingWhatItIsDoingNow(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 6, u32(9)))

	rows := liveRegion(t, s, out)
	if len(rows) != 1 {
		t.Fatalf("live region = %q, want the unit on one row", rows)
	}
	if !strings.Contains(rows[0], "web") || strings.Contains(rows[0], "web "+strings.TrimSpace(pathSep)) {
		t.Errorf("unit row = %q, want the unit alone in the name — its one phase adds nothing to it", rows[0])
	}
	if !strings.Contains(rows[0], "6/9") {
		t.Errorf("unit row = %q, want the counts the producer declared on the same row", rows[0])
	}
	if !elapsedTail.MatchString(rows[0]) {
		t.Errorf("unit row = %q, want the elapsed time last on the row", rows[0])
	}
}

var elapsedTail = regexp.MustCompile(`  (<1s|\d+s|\d+m\d\ds)$`)

func TestADetailIsCutSoTheElapsedTimeAlwaysFits(t *testing.T) {
	t.Parallel()
	var out safeBuffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: 60, Height: 40})
	t.Cleanup(func() { _ = s.Close() })

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, strings.Repeat("compiling every module in the project ", 4), 0, nil))

	rows := liveRegion(t, s, &out)
	if len(rows) != 1 {
		t.Fatalf("live region = %q, want the unit on one row", rows)
	}
	if got := len([]rune(rows[0])); got >= 60 {
		t.Errorf("unit row is %d columns wide, want it inside the terminal so it never wraps", got)
	}
	if !elapsedTail.MatchString(rows[0]) {
		t.Errorf("unit row = %q, want the detail cut back and the elapsed time kept", rows[0])
	}
}

func TestTheRendererNeverInventsCountsAProducerDidNotDeclare(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 0, nil))

	rows := liveRegion(t, s, out)
	if len(rows) != 1 {
		t.Fatalf("live region = %q, want the unit row alone", rows)
	}
	if strings.Contains(rows[0], "/") {
		t.Errorf("unit row = %q, want no counts when the producer declared no total", rows[0])
	}
}

func TestATwentyFirstUnitStaysOnScreenWhenTheTerminalIsTallEnough(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 60)
	spineOf(t, s, 21)

	rows := liveRegion(t, s, out)
	if len(rows) != 21 {
		t.Fatalf("live region has %d lines, want a row for all 21 units — no fixed cap", len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "app-21") {
		t.Errorf("live region = %q, want the twenty-first unit on screen", rows)
	}
}

func TestUnitsBeyondTheTerminalHeightFallIntoTheOverflowLineAndComeBack(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 9)
	spineOf(t, s, 21)

	rows := liveRegion(t, s, out)
	if len(rows) != 6 {
		t.Fatalf("live region = %q, want the window bounded by the terminal's nine rows", rows)
	}
	if got := rows[len(rows)-1]; !strings.Contains(got, "+16 more: 16 running") {
		t.Errorf("overflow line = %q, want the hidden units counted by state", got)
	}
	if strings.Contains(strings.Join(rows, "\n"), "app-21") {
		t.Fatalf("live region = %q, want app-21 below the fold", rows)
	}

	for i := 1; i <= 18; i++ {
		s.Emit(spanEvent(appStage(byte(2*i+1)), false, time.Second))
		s.Emit(spanEvent(appStage(byte(2*i)), false, time.Second))
	}

	if got := strings.Join(liveRegion(t, s, out), "\n"); !strings.Contains(got, "app-21") {
		t.Errorf("live region = %q, want app-21 back on screen once the finished units freed the space", got)
	}
}

func TestAUnitDeclaredButNotYetStartedIsAbsentFromTheWindow(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)
	spineOf(t, s, 1)

	s.Emit(stagePlanEvent(&progressv1.Stage{Id: appStage(60), Title: "api"}))

	rows := liveRegion(t, s, out)
	if len(rows) != 1 {
		t.Fatalf("live region = %q, want the running unit alone", rows)
	}
	if strings.Contains(strings.Join(rows, "\n"), "api") {
		t.Errorf("live region = %q, want a unit that has not started kept out of the window", rows)
	}
}

func TestTheOutputLineFollowsDeclarationOrderNotActivationOrder(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit := appStage(1)
	first, second, third := appStage(2), appStage(3), appStage(4)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: first, ParentId: unit, Title: "Building"},
		&progressv1.Stage{Id: second, ParentId: unit, Title: "Uploading"},
		&progressv1.Stage{Id: third, ParentId: unit, Title: "Provisioning"},
	))
	for _, id := range [][]byte{third, first, second} {
		s.Emit(progressEvent(id, "working", 0, nil))
	}

	rows := liveRegion(t, s, out)
	if len(rows) != 1 || !strings.Contains(rows[0], "web › Building") {
		t.Fatalf("live region = %q, want the first phase in declaration order named on the unit row", rows)
	}
}

func TestARunningBuildKeepsItsDetailWhileTheRestOfTheSpineWaits(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 20)
	spineOf(t, s, 2)
	for i := 1; i <= 14; i++ {
		s.Emit(stagePlanEvent(&progressv1.Stage{Id: appStage(byte(20 + i)), Title: fmt.Sprintf("waiting-%02d", i)}))
	}

	rows := liveRegion(t, s, out)
	if len(rows) != 2 {
		t.Fatalf("live region = %q, want a row for each running unit and nothing for the units that never started", rows)
	}
	if !strings.Contains(rows[0], "app-01") || !strings.Contains(rows[0], "6/9") {
		t.Fatalf("live region = %q, want the running build keeping its detail", rows)
	}
	if !strings.Contains(rows[1], "app-02") || !strings.Contains(rows[1], "6/9") {
		t.Errorf("live region = %q, want every running unit at the same height", rows)
	}
}

func TestAFinishedUnitLeavesTheWindow(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)
	spineOf(t, s, 2)

	s.Emit(spanEvent(appStage(3), false, time.Second))
	s.Emit(spanEvent(appStage(2), false, time.Second))

	rows := liveRegion(t, s, out)
	if got := strings.Join(rows, "\n"); strings.Contains(got, "app-01") {
		t.Fatalf("live region = %q, want the finished unit gone — its record is the block it flushed", rows)
	}
	if len(rows) != 1 || !strings.Contains(rows[0], "app-02") {
		t.Errorf("live region = %q, want the still-running unit alone in the window", rows)
	}

	out.Reset()
	s.Emit(progressEvent(appStage(5), "compiling", 8, u32(9)))
	if strings.Contains(out.String(), "app-01") {
		t.Errorf("frame = %q, want the finished unit left in scrollback, never redrawn", out.String())
	}
}

func TestTheOverflowLineCountsOnlyWhatIsStillOnTheSpine(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 9)
	spineOf(t, s, 8)

	s.Emit(spanEvent(appStage(3), false, time.Second))
	s.Emit(spanEvent(appStage(2), false, time.Second))

	rows := liveRegion(t, s, out)
	if got := rows[len(rows)-1]; !strings.Contains(got, "+2 more: 2 running") {
		t.Errorf("overflow line = %q, want the finished unit counted nowhere at all", got)
	}
}

func TestAFailedUnitStaysPinnedWhileItsSiblingsRun(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)
	spineOf(t, s, 3)

	s.Emit(spanEvent(appStage(3), true, time.Second))

	rows := liveRegion(t, s, out)
	if !strings.Contains(rows[0], failMark) || !strings.Contains(rows[0], "app-01") {
		t.Fatalf("live region = %q, want the failed unit pinned as a single row at the top", rows)
	}
	if strings.Contains(rows[1], "app-01") {
		t.Errorf("live region = %q, want the failed unit pinned on a row of its own", rows)
	}

	out.Reset()
	s.Emit(progressEvent(appStage(5), "compiling", 7, u32(9)))
	if rows := liveRegion(t, s, out); !strings.Contains(rows[0], "app-01") {
		t.Errorf("live region = %q, want the failure still pinned while a sibling makes progress", rows)
	}
}
