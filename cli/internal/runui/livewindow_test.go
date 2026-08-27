package runui

import (
	"fmt"
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

func TestAUnitGetsOneRowAndItsBuildShowsUnderIt(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 6, u32(9)))

	rows := liveRegion(t, s, out)
	if len(rows) != 2 {
		t.Fatalf("live region = %q, want one row for the unit and one output line under it", rows)
	}
	if !strings.Contains(rows[0], "web") || strings.Contains(rows[0], "Building") {
		t.Errorf("unit row = %q, want just the unit", rows[0])
	}
	if !strings.Contains(rows[1], "web › Building") || !strings.Contains(rows[1], "6/9") {
		t.Errorf("output line = %q, want the app's build with the counts the producer declared", rows[1])
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
	if len(rows) != 2 {
		t.Fatalf("live region = %q, want the unit row and its output line", rows)
	}
	if strings.Contains(rows[1], "/") {
		t.Errorf("output line = %q, want no counts when the producer declared no total", rows[1])
	}
}

func TestATwentyFirstUnitStaysOnScreenWhenTheTerminalIsTallEnough(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 60)
	spineOf(t, s, 21)

	rows := liveRegion(t, s, out)
	if len(rows) != 42 {
		t.Fatalf("live region has %d lines, want a row and an output line for all 21 units — no fixed cap", len(rows))
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

func TestAUnitDeclaredButNotYetStartedWaitsBelowTheRunningOnes(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)
	spineOf(t, s, 1)

	s.Emit(stagePlanEvent(&progressv1.Stage{Id: appStage(60), Title: "api"}))

	rows := liveRegion(t, s, out)
	if len(rows) != 3 {
		t.Fatalf("live region = %q, want the running unit, its output line, and the waiting unit", rows)
	}
	if !strings.Contains(rows[2], pendMark) || !strings.Contains(rows[2], "api") {
		t.Errorf("last row = %q, want the declared unit waiting under the running one", rows[2])
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
	if len(rows) != 2 || !strings.Contains(rows[1], "web › Building") {
		t.Fatalf("live region = %q, want the first child in declaration order on the output line", rows)
	}
}

func TestARunningBuildKeepsItsOutputLineWhileTheRestOfTheSpineWaits(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 20)
	spineOf(t, s, 2)
	for i := 1; i <= 14; i++ {
		s.Emit(stagePlanEvent(&progressv1.Stage{Id: appStage(byte(20 + i)), Title: fmt.Sprintf("waiting-%02d", i)}))
	}

	rows := liveRegion(t, s, out)
	if !strings.Contains(rows[1], "app-01 › Building") || !strings.Contains(rows[1], "6/9") {
		t.Fatalf("live region = %q, want the running build's output line — waiting units cannot cost the running tier its second line", rows)
	}
	if !strings.Contains(rows[3], "app-02 › Building") {
		t.Errorf("live region = %q, want every running unit at the same height", rows)
	}
	if got := rows[len(rows)-1]; !strings.Contains(got, "+2 more: 2 waiting") {
		t.Errorf("overflow line = %q, want the waiting units that did not fit counted below the fold", got)
	}
}

func TestAFinishedUnitRanksLastAsASingleRow(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)
	spineOf(t, s, 2)

	s.Emit(spanEvent(appStage(3), false, time.Second))
	s.Emit(spanEvent(appStage(2), false, time.Second))

	rows := liveRegion(t, s, out)
	if got := rows[len(rows)-1]; !strings.Contains(got, okMark) || !strings.Contains(got, "app-01") {
		t.Fatalf("live region = %q, want the finished unit ranked under the running one, not gone", rows)
	}
	if got := strings.Count(strings.Join(rows, "\n"), "app-01"); got != 1 {
		t.Errorf("live region = %q, want the finished unit as one row — its story is already in scrollback", rows)
	}

	out.Reset()
	s.Emit(progressEvent(appStage(5), "compiling", 8, u32(9)))
	if got := strings.Count(out.String(), "app-01"); got != 1 {
		t.Errorf("frame = %q, want the finished unit's row redrawn once and its committed block left in scrollback", out.String())
	}
}

func TestTheOverflowLineCountsFinishedUnits(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 9)
	spineOf(t, s, 8)

	s.Emit(spanEvent(appStage(3), false, time.Second))
	s.Emit(spanEvent(appStage(2), false, time.Second))

	rows := liveRegion(t, s, out)
	if got := rows[len(rows)-1]; !strings.Contains(got, "+3 more: 2 running · 1 done") {
		t.Errorf("overflow line = %q, want the finished unit counted below the fold", got)
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
		t.Errorf("live region = %q, want the failed unit pinned without an output line", rows)
	}

	out.Reset()
	s.Emit(progressEvent(appStage(5), "compiling", 7, u32(9)))
	if rows := liveRegion(t, s, out); !strings.Contains(rows[0], "app-01") {
		t.Errorf("live region = %q, want the failure still pinned while a sibling makes progress", rows)
	}
}
