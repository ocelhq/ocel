package runui

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func u32(n uint32) *uint32 { return &n }

func appStage(n byte) []byte {
	return []byte{n, 0, 0, 0, 0, 0, 0, 0}
}

func liveStream(t *testing.T) (*Stream, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight})
	t.Cleanup(func() { _ = s.Close() })
	return s, &out
}

func liveStreamOfHeight(t *testing.T, height int) (*Stream, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: 200, Height: height})
	t.Cleanup(func() { _ = s.Close() })
	return s, &out
}

func liveRegion(t *testing.T, s *Stream, out *bytes.Buffer) []string {
	t.Helper()
	s.r.Pause()
	out.Reset()
	s.r.Resume()

	drawn := out.String()
	out.Reset()
	s.r.Pause()
	if want := fmt.Sprintf("\033[%dA\033[J", strings.Count(drawn, "\n")); drawn != "" && out.String() != want {
		t.Fatalf("erase = %q, want %q — the renderer must take back exactly the lines it drew", out.String(), want)
	}
	out.Reset()
	s.r.Resume()

	if drawn == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(drawn, "\n"), "\n")
}

func stagePlanEvent(stages ...*progressv1.Stage) *streamv1.RunEvent {
	return operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{
		StagePlan: &progressv1.StagePlanEvent{Stages: stages},
	}})
}

func progressEvent(stageID []byte, message string, current uint32, total *uint32) *streamv1.RunEvent {
	ev := &progressv1.ProgressEvent{StageId: stageID, Message: message, Total: total}
	if total != nil {
		ev.Current = &current
	}
	return operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{Progress: ev}})
}

func spanEvent(stageID []byte, failed bool, d time.Duration) *streamv1.RunEvent {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if failed {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
	}
	return operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
		Span: &progressv1.SpanEvent{
			SpanId:            stageID,
			StartTimeUnixNano: 1,
			EndTimeUnixNano:   int64(d) + 1,
			Status:            status,
		},
	}})
}

func diagnosticEvent(message string) *streamv1.RunEvent {
	return &streamv1.RunEvent{Event: &streamv1.RunEvent_Diagnostic{
		Diagnostic: &streamv1.DiagnosticEvent{Message: message, Level: streamv1.DiagnosticLevel_DIAGNOSTIC_LEVEL_INFO},
	}}
}

func TestAnInRunNoticeIsCommittedAboveALiveFrameThatStillErasesExactly(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 6, u32(9)))

	const notice = "Serving previews on the global preview domain *.previews.ocel.dev"
	s.Emit(diagnosticEvent(notice))

	if !strings.Contains(out.String(), notice) {
		t.Errorf("stdout = %q, want the notice committed as stream content", out.String())
	}
	rows := liveRegion(t, s, out)
	if len(rows) != 1 {
		t.Fatalf("live region = %q, want the unit's row still standing after a notice landed", rows)
	}
	for _, row := range rows {
		if strings.Contains(row, notice) {
			t.Errorf("live row = %q, want the notice committed to the scrollback, not drawn into the window", row)
		}
	}
}

func TestASpinnerRaisedThroughTheRunUIBecomesARowOfTheLiveFrame(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 6, u32(9)))

	spinner := s.Spin("Checking credentials")
	rows := liveRegion(t, s, out)
	if len(rows) != 2 || !strings.Contains(rows[1], "Checking credentials") {
		t.Fatalf("live region = %q, want the spinner drawn as the frame's own last row", rows)
	}

	spinner.Stop()
	if rows := liveRegion(t, s, out); len(rows) != 1 {
		t.Errorf("live region = %q, want the spinner row gone once it stops", rows)
	}
}

func TestNoRawSpinnerCanTouchATerminalALiveFrameOwns(t *testing.T) {
	t.Parallel()
	s, out := liveStreamOfHeight(t, 40)

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "compiling", 6, u32(9)))

	var elsewhere bytes.Buffer
	spinner := StartSpinner(Presentation{Format: FormatHuman, TTY: true, Width: 200, Height: 40}, &elsewhere, "Checking credentials")
	time.Sleep(3 * frameRate)
	spinner.Stop()

	if elsewhere.Len() != 0 {
		t.Errorf("the fallback spinner wrote %q, want it inert while a live frame owns the terminal", elsewhere.String())
	}
	if rows := liveRegion(t, s, out); len(rows) != 1 {
		t.Errorf("live region = %q, want the frame untouched by a spinner raised behind its back", rows)
	}
}

func TestSuspendClearsTheLiveRegionAndPutsItBack(t *testing.T) {
	t.Parallel()
	s, out := liveStream(t)
	r := s.r

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "app-a"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Uploading"},
	))
	s.Emit(progressEvent(phase, "uploading assets", 1, u32(10)))

	resume := r.Suspend()
	if r.liveLines != 0 {
		t.Errorf("liveLines = %d, want the live region erased before anything else takes the terminal", r.liveLines)
	}

	out.Reset()
	s.Emit(progressEvent(phase, "uploading assets", 2, u32(10)))
	if out.Len() != 0 {
		t.Errorf("wrote %q while suspended, want the terminal left to the prompt", out.String())
	}

	resume()
	if !strings.Contains(out.String(), "app-a") {
		t.Errorf("after resuming, out = %q, want the live region drawn again", out.String())
	}
}

func TestTheRegionThatRedrawsInPlace(t *testing.T) {
	t.Run("a parallel deploy shows one line per app, each with its own stage", func(t *testing.T) {
		t.Parallel()
		s, out := liveStream(t)

		appA, appB := appStage(1), appStage(2)
		s.Emit(stagePlanEvent(
			&progressv1.Stage{Id: appA, Title: "app-a"},
			&progressv1.Stage{Id: appB, Title: "app-b"},
		))
		s.Emit(progressEvent(appA, "uploading assets", 1, u32(10)))
		s.Emit(progressEvent(appB, "uploading assets", 9, u32(10)))

		got := out.String()
		if !strings.Contains(got, "app-a") || !strings.Contains(got, "app-b") {
			t.Fatalf("live region = %q, want a line for both app-a and app-b", got)
		}
		if active := s.r.plan.activeOrder; len(active) != 2 {
			t.Fatalf("activeOrder = %v, want both apps live at once", active)
		}
	})

	t.Run("one app finishing does not stop the other's line from updating", func(t *testing.T) {
		t.Parallel()
		s, out := liveStream(t)

		appA, appB := appStage(1), appStage(2)
		s.Emit(stagePlanEvent(
			&progressv1.Stage{Id: appA, Title: "app-a"},
			&progressv1.Stage{Id: appB, Title: "app-b"},
		))
		s.Emit(progressEvent(appA, "uploading", 1, u32(2)))
		s.Emit(progressEvent(appB, "uploading", 1, u32(2)))
		s.Emit(spanEvent(appA, false, time.Second))

		rows := liveRegion(t, s, out)
		if len(rows) != 1 || !strings.Contains(rows[0], "app-b") {
			t.Fatalf("live region = %q, want app-b still spinning with the finished app-a gone from the window", rows)
		}

		s.Emit(spanEvent(appB, false, time.Second))
		if rows := liveRegion(t, s, out); len(rows) != 0 {
			t.Errorf("live region = %q, want nothing left in flight to show", rows)
		}
	})
}

func TestOrphanStageAttachesRecursively(t *testing.T) {
	t.Parallel()
	plan := newStagePlan()
	grandparent, parent, child := appStage(1), appStage(2), appStage(3)

	plan.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
		{Id: child, ParentId: parent, Title: "child"},
	}})
	plan.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
		{Id: parent, ParentId: grandparent, Title: "parent"},
	}})
	plan.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
		{Id: grandparent, Title: "grandparent"},
	}})

	if len(plan.roots) != 1 || plan.roots[0] != stageKey(grandparent) {
		t.Fatalf("roots = %v, want just grandparent", plan.roots)
	}
	if got := plan.nodes[stageKey(grandparent)].children; len(got) != 1 || got[0] != stageKey(parent) {
		t.Fatalf("grandparent.children = %v, want [parent]", got)
	}
	if got := plan.nodes[stageKey(parent)].children; len(got) != 1 || got[0] != stageKey(child) {
		t.Fatalf("parent.children = %v, want [child]", got)
	}
}

func TestColourIsDecidedFromTheTargetWriter(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	unit, phase := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "Environment"},
		&progressv1.Stage{Id: phase, ParentId: unit, Title: "Building"},
	))
	s.Emit(progressEvent(phase, "uploading", 1, u32(2)))
	s.Emit(spanEvent(phase, false, time.Second))
	s.Emit(&streamv1.RunEvent{Event: &streamv1.RunEvent_Result{Result: &streamv1.RunResultEvent{
		Success: true, Headline: "Deployed", Apps: []*progressv1.AppResult{{App: "web", Urls: []string{"https://app.example.workers.dev"}}},
	}}})

	if got := out.String(); strings.Contains(got, "\x1b[") {
		t.Errorf("output = %q, want no ANSI escape codes when the writer is not a terminal", got)
	}
}

func TestRendererSingleOwnerRaceFree(t *testing.T) {
	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight})

	appA, appB := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: appA, Title: "app-a"},
		&progressv1.Stage{Id: appB, Title: "app-b"},
	))

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := uint32(0); i < 50; i++ {
			s.Emit(progressEvent(appA, "uploading", i, u32(50)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := uint32(0); i < 50; i++ {
			s.Emit(progressEvent(appB, "uploading", i, u32(50)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.Emit(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
				Log: &progressv1.LogEvent{StageId: appA, Message: fmt.Sprintf("subprocess output line %d", i)},
			}}))
		}
	}()

	wg.Wait()
	time.Sleep(3 * frameRate)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestFormatDurationRoundsToWholeSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{40 * time.Millisecond, "<1s"},
		{499 * time.Millisecond, "<1s"},
		{500 * time.Millisecond, "1s"},
		{1100 * time.Millisecond, "1s"},
		{1600 * time.Millisecond, "2s"},
		{59*time.Second + 600*time.Millisecond, "1m00s"},
		{90 * time.Second, "1m30s"},
		{-2 * time.Second, "0s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestAPhaseCommitsWhenItsSpanArrives(t *testing.T) {
	t.Parallel()
	s, out := liveStream(t)

	unit, slow, quick := appStage(1), appStage(2), appStage(3)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: quick, ParentId: unit, Title: "Uploading"},
		&progressv1.Stage{Id: slow, ParentId: unit, Title: "Provisioning"},
	))
	s.Emit(progressEvent(quick, "uploading assets", 0, nil))
	s.Emit(progressEvent(slow, "provisioning", 0, nil))

	s.Emit(spanEvent(quick, false, 90*time.Second))
	got := out.String()
	if !strings.Contains(got, okMark+" web › Uploading  1m30s") {
		t.Errorf("output = %q, want the phase committed with its span's own duration", got)
	}
	if strings.Contains(got, okMark+" web › Provisioning") {
		t.Errorf("output = %q, want the unfinished phase left uncommitted", got)
	}

	s.Emit(spanEvent(slow, true, time.Second))
	if got := out.String(); !strings.Contains(got, failMark+" web › Provisioning failed") {
		t.Errorf("output = %q, want the failed phase committed as failed", got)
	}
}

func TestAPhaseRowStaysLiveUntilItsSpanArrives(t *testing.T) {
	t.Parallel()
	s, out := liveStream(t)

	unit, uploading := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: unit, Title: "web"},
		&progressv1.Stage{Id: uploading, ParentId: unit, Title: "Uploading"},
	))
	s.Emit(progressEvent(uploading, "Uploading function artifacts", 1, u32(1)))

	if got := strings.Count(out.String(), okMark); got != 0 {
		t.Fatalf("committed %d finished lines at 1/1, want none — only the stage's span ends it", got)
	}

	s.Emit(spanEvent(uploading, false, 12*time.Second))
	if got := out.String(); !strings.Contains(got, okMark+" web  12s") {
		t.Errorf("scrollback = %q, want the finished phase committed as the unit's block", got)
	}
	if rows := liveRegion(t, s, out); len(rows) != 0 {
		t.Errorf("live region = %q, want the phase's spinning row taken back by its own span", rows)
	}
}

func TestChildStageHoldsUnderItsParentUntilTheParentEnds(t *testing.T) {
	t.Parallel()
	s, out := liveStream(t)
	r := s.r

	provisioning, app := appStage(1), appStage(2)
	s.Emit(stagePlanEvent(
		&progressv1.Stage{Id: provisioning, Title: "Provisioning"},
		&progressv1.Stage{Id: app, ParentId: provisioning, Title: "next-test"},
	))
	s.Emit(progressEvent(provisioning, "Reconciling the edge stack", 0, nil))
	s.Emit(progressEvent(app, "creating resources", 0, nil))

	rows := liveRegion(t, s, out)
	if len(rows) != 1 || !strings.Contains(rows[0], "Provisioning") || !strings.Contains(rows[0], "next-test") {
		t.Fatalf("live region = %q, want next-test named as the detail of the unit's row", rows)
	}

	s.Emit(spanEvent(app, false, 44*time.Second))
	if !r.plan.isActive(stageKey(app)) {
		t.Fatal("want the finished child held in the live region under its still-running parent")
	}
	if rows := liveRegion(t, s, out); len(rows) != 1 || !strings.Contains(rows[0], "Provisioning") {
		t.Fatalf("live region = %q, want the unit still running on its own row once the child finished", rows)
	}

	s.Emit(spanEvent(provisioning, false, 50*time.Second))
	if active := r.plan.activeOrder; len(active) != 1 || active[0] != stageKey(provisioning) {
		t.Fatalf("activeOrder = %v, want the subtree folded into the unit's own done row", active)
	}
	if rows := liveRegion(t, s, out); len(rows) != 0 {
		t.Fatalf("live region = %q, want the finished unit gone from the window", rows)
	}
}

func TestRawEngineOutputIsShownOnlyWhenVerboseOrWhenThePhaseFailed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		present Presentation
		failed  bool
		want    bool
	}{
		{"a live terminal", Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight}, false, false},
		{"a pipe", Presentation{Format: FormatHuman, Width: defaultWidth}, false, false},
		{"--verbose", Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth}, false, true},
		{"a live terminal whose phase failed", Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight}, true, true},
		{"a pipe whose phase failed", Presentation{Format: FormatHuman, Width: defaultWidth}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			s := NewStream(&out, tc.present)
			t.Cleanup(func() { _ = s.Close() })

			unit, phase := appStage(1), appStage(2)
			s.Emit(stagePlanEvent(
				&progressv1.Stage{Id: unit, Title: "web"},
				&progressv1.Stage{Id: phase, ParentId: unit, Title: "Provisioning"},
			))
			s.Emit(progressEvent(phase, "creating bucket assets", 0, nil))
			s.Emit(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
				Log: &progressv1.LogEvent{StageId: phase, Message: "@ updating....."},
			}}))
			if strings.Contains(out.String(), "@ updating.....") {
				t.Fatalf("output = %q, want the line held in its block until the phase completes", out.String())
			}

			s.Emit(spanEvent(phase, tc.failed, time.Second))
			got := out.String()
			if strings.Contains(got, "  @ updating.....") != tc.want {
				t.Errorf("output = %q, want the raw engine line shown = %v", got, tc.want)
			}
			if !strings.Contains(got, "  creating bucket assets") {
				t.Errorf("output = %q, want what the phase said about itself kept whatever the verbosity", got)
			}
		})
	}
}

func TestProgressWithoutAStageIsDropped(t *testing.T) {
	t.Parallel()
	s, out := liveStream(t)

	s.Emit(progressEvent(nil, "Reclaimed 3 promotion(s): a, b, c", 0, nil))

	if got := len(s.r.plan.nodes); got != 0 {
		t.Fatalf("got %d nodes, want none: there is no bucket for progress that belongs to no stage", got)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing drawn for a stageless progress event", out.String())
	}
}
