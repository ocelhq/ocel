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
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth})
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

func liveRegion(s *Stream, out *bytes.Buffer) []string {
	written := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.r.liveLines > len(written) {
		return written
	}
	return written[len(written)-s.r.liveLines:]
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
	if !strings.Contains(out.String(), "Uploading") {
		t.Errorf("after resuming, out = %q, want the live region drawn again", out.String())
	}
}

func TestLiveRegion(t *testing.T) {
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
		s, _ := liveStream(t)

		appA, appB := appStage(1), appStage(2)
		s.Emit(stagePlanEvent(
			&progressv1.Stage{Id: appA, Title: "app-a"},
			&progressv1.Stage{Id: appB, Title: "app-b"},
		))
		s.Emit(progressEvent(appA, "uploading", 1, u32(2)))
		s.Emit(progressEvent(appB, "uploading", 1, u32(2)))
		s.Emit(spanEvent(appA, false, time.Second))

		active := s.r.plan.activeOrder
		if len(active) != 1 || active[0] != stageKey(appB) {
			t.Fatalf("activeOrder = %v, want only app-b still live", active)
		}

		s.Emit(spanEvent(appB, false, time.Second))
		if active := s.r.plan.activeOrder; len(active) != 0 {
			t.Errorf("activeOrder = %v, want both apps finished", active)
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
		Success: true, Headline: "Deployed", AppUrls: []string{"https://app.example.workers.dev"},
	}}})

	if got := out.String(); strings.Contains(got, "\x1b[") {
		t.Errorf("output = %q, want no ANSI escape codes when the writer is not a terminal", got)
	}
}

func TestRendererSingleOwnerRaceFree(t *testing.T) {
	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth})

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
	if active := s.r.plan.activeOrder; len(active) != 0 {
		t.Errorf("activeOrder = %v, want the phase row retired by its own span", active)
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

	rows := liveRegion(s, out)
	if len(rows) != 2 || !strings.Contains(rows[0], "Provisioning") || !strings.Contains(rows[1], "Provisioning › next-test") {
		t.Fatalf("live region = %q, want next-test as the unit's output line", rows)
	}

	s.Emit(spanEvent(app, false, 44*time.Second))
	if !r.plan.isActive(stageKey(app)) {
		t.Fatal("want the finished child held in the live region under its still-running parent")
	}
	if rows := liveRegion(s, out); len(rows) != 1 || !strings.Contains(rows[0], "Provisioning") {
		t.Fatalf("live region = %q, want the unit down to its own row once the child finished", rows)
	}

	s.Emit(spanEvent(provisioning, false, 50*time.Second))
	if active := r.plan.activeOrder; len(active) != 0 {
		t.Fatalf("activeOrder = %v, want the whole subtree retired with the parent", active)
	}
}

func TestRawLogLinesFlushInsideTheirPhaseBlockAtEveryVerbosity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		present Presentation
	}{
		{"a live terminal", Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth}},
		{"a pipe", Presentation{Format: FormatHuman, Width: defaultWidth}},
		{"--verbose", Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth}},
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
			s.Emit(operation(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
				Log: &progressv1.LogEvent{StageId: phase, Message: "pulumi engine line"},
			}}))
			if strings.Contains(out.String(), "pulumi engine line") {
				t.Fatalf("output = %q, want the line held in its block until the phase completes", out.String())
			}

			s.Emit(spanEvent(phase, false, time.Second))
			if !strings.Contains(out.String(), "  pulumi engine line") {
				t.Errorf("output = %q, want the raw line flushed inside its phase block", out.String())
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
