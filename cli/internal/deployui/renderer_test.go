package deployui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func u32(n uint32) *uint32 { return &n }

func appStage(n byte) []byte {
	return []byte{n, 0, 0, 0, 0, 0, 0, 0}
}

func TestLiveRegion(t *testing.T) {
	t.Run("a parallel deploy shows one line per app, each with its own stage", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, true, false)
		t.Cleanup(func() { _ = r.Close() })

		appA, appB := appStage(1), appStage(2)
		r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
			{Id: appA, Title: "app-a"},
			{Id: appB, Title: "app-b"},
		}, Final: true})

		r.Progress(appA, deploymentsv1.Phase_PHASE_UPLOADING, "uploading assets", 1, u32(10))
		r.Progress(appB, deploymentsv1.Phase_PHASE_UPLOADING, "uploading assets", 9, u32(10))

		got := out.String()
		if !strings.Contains(got, "app-a") || !strings.Contains(got, "app-b") {
			t.Fatalf("live region = %q, want a line for both app-a and app-b", got)
		}

		if len(r.plan.activeOrder) != 2 {
			t.Fatalf("activeOrder = %v, want both apps live at once", r.plan.activeOrder)
		}
	})

	t.Run("one app finishing does not stop the other's line from updating", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, true, false)
		t.Cleanup(func() { _ = r.Close() })

		appA, appB := appStage(1), appStage(2)
		r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
			{Id: appA, Title: "app-a"},
			{Id: appB, Title: "app-b"},
		}, Final: true})

		r.Progress(appA, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 1, u32(2))
		r.Progress(appB, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 1, u32(2))
		r.StageEnd(appA, false, time.Second) // app-a finishes

		if len(r.plan.activeOrder) != 1 || r.plan.activeOrder[0] != stageKey(appB) {
			t.Fatalf("activeOrder = %v, want only app-b still live", r.plan.activeOrder)
		}
		if got := out.String(); !strings.Contains(got, "app-a") {
			t.Errorf("output = %q, want app-a's finished line committed to scrollback", got)
		}

		r.StageEnd(appB, false, time.Second)
		if len(r.plan.activeOrder) != 0 {
			t.Errorf("activeOrder = %v, want both apps finished", r.plan.activeOrder)
		}
	})

	t.Run("which app is stuck stays answerable: a slow app keeps its own row", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, true, false)
		t.Cleanup(func() { _ = r.Close() })

		fast, slow := appStage(1), appStage(2)
		r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
			{Id: fast, Title: "fast-app"},
			{Id: slow, Title: "slow-app"},
		}, Final: true})

		r.Progress(fast, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 1, u32(1))
		r.Progress(slow, deploymentsv1.Phase_PHASE_UPLOADING, "still going", 1, u32(10))
		r.StageEnd(fast, false, time.Second)

		if len(r.plan.activeOrder) != 1 || r.plan.activeOrder[0] != stageKey(slow) {
			t.Fatalf("activeOrder = %v, want only slow-app still shown as live", r.plan.activeOrder)
		}
	})
}

func TestOrphanStageAttachesRecursively(t *testing.T) {
	t.Parallel()
	plan := newStagePlan()
	grandparent, parent, child := appStage(1), appStage(2), appStage(3)

	plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: child, ParentId: parent, Title: "child"},
	}})
	plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: parent, ParentId: grandparent, Title: "parent"},
	}})
	plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
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

func TestStagePlanFinal(t *testing.T) {
	t.Parallel()
	plan := newStagePlan()
	if plan.final {
		t.Fatal("a fresh plan is already final")
	}
	plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{{Id: appStage(1), Title: "a"}}})
	if plan.final {
		t.Error("plan.final became true without an event saying so — a plan that can still grow must not claim to be done")
	}
	plan.apply(&deploymentsv1.StagePlanEvent{Final: true})
	if !plan.final {
		t.Error("plan.final stayed false after an event with Final: true")
	}
}

func TestColourIsDecidedFromTheTargetWriter(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer // not a *os.File, so never a terminal regardless of the test process's own stdout
	r := NewRenderer(&out, FormatHuman, false)
	t.Cleanup(func() { _ = r.Close() })

	r.Building()
	r.Progress(nil, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 1, u32(2))
	r.Deployed("Deployed", []string{"https://app.example.workers.dev"}, Flip{}, "")

	if got := out.String(); strings.Contains(got, "\x1b[") {
		t.Errorf("output = %q, want no ANSI escape codes when the writer is not a terminal", got)
	}
}

// TestRendererSingleOwnerRaceFree writes into a plain, unsynchronized
// bytes.Buffer from three concurrent goroutines — one repaint tick loop
// (started by the live renderer) plus two callers, mirroring the subprocess
// drain goroutines a real deploy has. It is the renderer's own mutex being
// the only thing standing between this and a torn write, so it must be run
// with -race.
func TestRendererSingleOwnerRaceFree(t *testing.T) {
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)

	appA, appB := appStage(1), appStage(2)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: appA, Title: "app-a"},
		{Id: appB, Title: "app-b"},
	}, Final: true})

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := uint32(0); i < 50; i++ {
			r.Progress(appA, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", i, u32(50))
		}
	}()
	go func() {
		defer wg.Done()
		for i := uint32(0); i < 50; i++ {
			r.Progress(appB, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", i, u32(50))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			fmt.Fprintf(r, "subprocess output line %d\n", i)
		}
	}()

	wg.Wait()
	time.Sleep(3 * frameRate)
	if err := r.Close(); err != nil {
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

func TestRestartBuildStageDiscardsElapsedTime(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	r.useClock(func() time.Time { return time.Unix(0, nowNanos.Load()) })

	r.Building()

	nowNanos.Add(int64(90 * time.Second))
	r.RestartBuildStage()

	nowNanos.Add(int64(1 * time.Second))
	r.BuildOK()

	got := out.String()
	if !strings.Contains(got, "1s") {
		t.Errorf("output = %q, want the committed Building line to show 1s, the successful attempt's own duration", got)
	}
	if strings.Contains(got, "91s") || strings.Contains(got, "1m3") {
		t.Errorf("output = %q, want the discarded attempt and the wait excluded from the displayed duration", got)
	}
}

func TestStageEndCommitsRowsAsSpansArrive(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	appA, appB := appStage(1), appStage(2)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: appA, Title: "app-a"},
		{Id: appB, Title: "app-b"},
	}, Final: true})

	r.Progress(appA, deploymentsv1.Phase_PHASE_PROVISIONING, "provisioning", 0, nil)
	r.Progress(appB, deploymentsv1.Phase_PHASE_PROVISIONING, "provisioning", 0, nil)

	r.StageEnd(appA, false, 90*time.Second)
	if len(r.plan.activeOrder) != 1 || r.plan.activeOrder[0] != stageKey(appB) {
		t.Fatalf("activeOrder = %v, want app-a committed the moment its span arrived", r.plan.activeOrder)
	}
	if got := out.String(); !strings.Contains(got, "app-a") || !strings.Contains(got, "1m30s") {
		t.Errorf("output = %q, want app-a committed with the span's own duration", got)
	}

	r.StageEnd(appB, true, time.Second)
	if len(r.plan.activeOrder) != 0 {
		t.Fatalf("activeOrder = %v, want app-b committed by its error span", r.plan.activeOrder)
	}
	if got := out.String(); !strings.Contains(got, "app-b failed") {
		t.Errorf("output = %q, want app-b committed as failed", got)
	}

	r.StageEnd([]byte{9, 9, 9, 9, 9, 9, 9, 9}, false, time.Second)
}

func TestFinishedBarWaitsForItsSpan(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	uploading := appStage(1)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: uploading, Title: "Uploading"},
	}, Final: true})

	r.Progress(uploading, deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", 1, u32(1))
	if len(r.plan.activeOrder) != 1 {
		t.Fatalf("activeOrder = %v, want the row still live at 1/1 — only the stage's span ends it", r.plan.activeOrder)
	}

	r.Progress(uploading, deploymentsv1.Phase_PHASE_UPLOADING, "Uploading static assets", 0, nil)
	if got := strings.Count(out.String(), okMark); got != 0 {
		t.Fatalf("got %d committed lines before the span arrived, want 0", got)
	}

	r.StageEnd(uploading, false, 12*time.Second)
	if len(r.plan.activeOrder) != 0 {
		t.Errorf("activeOrder = %v, want the row committed by its span", r.plan.activeOrder)
	}
}

func TestChildStageHoldsUnderItsParentUntilTheParentEnds(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	provisioning, app := appStage(1), appStage(2)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: provisioning, Title: "Provisioning"},
		{Id: app, ParentId: provisioning, Title: "next-test"},
	}, Final: true})

	r.Progress(provisioning, deploymentsv1.Phase_PHASE_PROVISIONING, "Reconciling the edge stack", 0, nil)
	r.Progress(app, deploymentsv1.Phase_PHASE_PROVISIONING, "creating resources", 0, nil)

	rows := r.plan.displayRows()
	if len(rows) != 2 || rows[0].n.title != "Provisioning" || rows[1].n.title != "next-test" || rows[1].depth != 1 {
		t.Fatalf("displayRows = %+v, want next-test indented under Provisioning", rows)
	}

	r.StageEnd(app, false, 44*time.Second)
	if !r.plan.isActive(stageKey(app)) {
		t.Fatal("want the finished child held in the live region under its still-running parent, not committed to scrollback above it")
	}
	if !strings.Contains(out.String(), "  "+okMark+" next-test") {
		t.Fatalf("output = %q, want the held child drawn with its result mark, indented under the parent", out.String())
	}

	r.StageEnd(provisioning, false, 50*time.Second)
	if len(r.plan.activeOrder) != 0 {
		t.Fatalf("activeOrder = %v, want the whole subtree committed with the parent", r.plan.activeOrder)
	}
	got := out.String()
	final := got[strings.LastIndex(got, "\x1b[J")+len("\x1b[J"):]
	parent := strings.Index(final, okMark+" Provisioning")
	child := strings.Index(final, "  "+okMark+" next-test")
	if parent == -1 || child == -1 || child < parent {
		t.Errorf("final block = %q, want the parent committed first with the child indented beneath it", final)
	}
	if !strings.Contains(final, "44s") || !strings.Contains(final, "50s") {
		t.Errorf("final block = %q, want each line stamped with its own span duration", final)
	}
}

func TestBuildOKLeavesNoStaleSpinnerRow(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	r.Building()
	r.BuildOK()

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, okMark+" Building project") {
		t.Errorf("last line = %q, want the committed Building line to be the final output — anything after it is a stale live row", last)
	}
	if !strings.Contains(last, "\033[") {
		t.Errorf("output = %q, want the live spinner row erased before the committed line was printed", out.String())
	}
}

func TestLiveModeHoldsBackRawLogLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	r.Progress(appStage(1), deploymentsv1.Phase_PHASE_PROVISIONING, "provisioning", 0, nil)
	r.Log("pulumi engine line")

	if strings.Contains(out.String(), "pulumi engine line") {
		t.Errorf("output = %q, want raw Log lines held back from the live view — they belong to the run log", out.String())
	}
}

func TestUntaggedProgressCommitsEachMessageAsItsOwnLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	r.Progress(nil, deploymentsv1.Phase_PHASE_UNSPECIFIED, "Reclaimed 3 promotion(s): a, b, c", 0, nil)
	r.Progress(nil, deploymentsv1.Phase_PHASE_UNSPECIFIED, "Kept 2 promotion(s).", 0, nil)

	if len(r.plan.nodes) != 1 {
		t.Fatalf("got %d untagged nodes, want 1: untagged progress must share a single node, not one per message", len(r.plan.nodes))
	}

	got := out.String()
	if !strings.Contains(got, "Reclaimed 3 promotion(s): a, b, c") {
		t.Errorf("output = %q, want the first untagged message committed to scrollback before the second overwrote it", got)
	}
	if !strings.Contains(got, "Kept 2 promotion(s).") {
		t.Errorf("output = %q, want the second untagged message live or committed", got)
	}
	if got := strings.Count(out.String(), okMark); got != 1 {
		t.Errorf("got %d committed (checkmarked) lines, want 1: the first message committed when the second arrived, the second is still live", got)
	}
}

func TestRepeatedUntaggedMessageStaysOneLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	for i := uint32(1); i <= 4; i++ {
		r.Progress(nil, deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", i, u32(4))
	}

	if got := strings.Count(out.String(), okMark); got != 0 {
		t.Errorf("got %d committed lines, want 0: a repeat of the same untagged message updates the live row instead of committing a duplicate", got)
	}

	r.Progress(nil, deploymentsv1.Phase_PHASE_UPLOADING, "Uploading static assets", 0, nil)
	if got := strings.Count(out.String(), okMark); got != 1 {
		t.Errorf("got %d committed lines, want 1: only the change of message commits the outgoing row", got)
	}
}

func TestFailedSpanNamesAStageThatNeverReportedProgress(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	app := appStage(1)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: app, Title: "app-a"},
	}, Final: true})

	r.StageEnd(app, true, time.Second)

	if got := out.String(); !strings.Contains(got, failMark+" app-a failed") {
		t.Errorf("output = %q, want the failed stage named even though it never reported progress", got)
	}
}

func TestParentSpanLeavesAStillRunningChildLive(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	parent, child := appStage(1), appStage(2)
	r.StagePlan(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
		{Id: parent, Title: "Provisioning"},
		{Id: child, ParentId: parent, Title: "next-test"},
	}, Final: true})

	r.Progress(parent, deploymentsv1.Phase_PHASE_PROVISIONING, "reconciling", 0, nil)
	r.Progress(child, deploymentsv1.Phase_PHASE_PROVISIONING, "creating resources", 0, nil)

	r.StageEnd(parent, false, 50*time.Second)
	if len(r.plan.activeOrder) != 1 || r.plan.activeOrder[0] != stageKey(child) {
		t.Fatalf("activeOrder = %v, want the still-running child left live rather than force-committed as succeeded", r.plan.activeOrder)
	}

	r.StageEnd(child, true, time.Second)
	if len(r.plan.activeOrder) != 0 {
		t.Fatalf("activeOrder = %v, want the child committed by its own span", r.plan.activeOrder)
	}
	if got := out.String(); !strings.Contains(got, failMark+" next-test failed") {
		t.Errorf("output = %q, want the child's late error span rendered as a failed row", got)
	}
}

func TestDeployedFlip(t *testing.T) {
	t.Run("the human render sets the note off from the urls, indented", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := NewRenderer(&out, FormatHuman, false)
		t.Cleanup(func() { _ = r.Close() })

		r.Deployed("Deployed", []string{"https://app.example.workers.dev"}, Flip{Note: "propagates within ~5 s"}, "")

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		idx := slices.Index(lines, "  propagates within ~5 s")
		if idx < 2 {
			t.Fatalf("output = %q, want an indented flip note line below the urls", out.String())
		}
		if lines[idx-1] != "" {
			t.Errorf("line before the note = %q, want a blank line separating it from the urls", lines[idx-1])
		}
		if lines[idx-2] != "  https://app.example.workers.dev" {
			t.Errorf("line above the blank = %q, want the app url", lines[idx-2])
		}
	})

	t.Run("a note-less bound renders nothing extra", func(t *testing.T) {
		t.Parallel()
		var withBound, without bytes.Buffer
		for _, tc := range []struct {
			out  *bytes.Buffer
			flip Flip
		}{
			{&withBound, Flip{Bound: &deploymentsv1.FlipBound{}}},
			{&without, Flip{}},
		} {
			r := NewRenderer(tc.out, FormatHuman, false)
			r.Deployed("Deployed", []string{"https://app.example.workers.dev"}, tc.flip, "")
			_ = r.Close()
		}
		if withBound.String() != without.String() {
			t.Errorf("output for an instant bound = %q, want it identical to no bound at all (%q)", withBound.String(), without.String())
		}
	})

	t.Run("json carries the bound as numbers, never as prose", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := NewRenderer(&out, FormatJSON, false)
		t.Cleanup(func() { _ = r.Close() })

		r.Deployed("Deployed", nil, Flip{
			Note:  "propagates within ~5 s",
			Bound: &deploymentsv1.FlipBound{TypicalMs: 5000, Published: true},
		}, "")

		if strings.Contains(out.String(), "propagates") {
			t.Errorf("json = %q, want the machine surface free of rendered English", out.String())
		}
		var rec struct {
			FlipBound *struct {
				TypicalMs float64 `json:"typicalMs"`
				Published bool    `json:"published"`
			} `json:"flipBound"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
			t.Fatalf("stdout = %q is not JSON: %v", out.String(), err)
		}
		if rec.FlipBound == nil {
			t.Fatalf("json = %q, want a structured flipBound", out.String())
		}
		if rec.FlipBound.TypicalMs != 5000 || !rec.FlipBound.Published {
			t.Errorf("flipBound = %+v, want 5000 ms published", *rec.FlipBound)
		}
	})

	t.Run("json omits the bound when none was recorded", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := NewRenderer(&out, FormatJSON, false)
		t.Cleanup(func() { _ = r.Close() })

		r.Deployed("Deployed", nil, Flip{}, "")

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
			t.Fatalf("stdout = %q is not JSON: %v", out.String(), err)
		}
		if _, ok := rec["flipBound"]; ok {
			t.Errorf("json = %q, want no flipBound key when the provider recorded none", out.String())
		}
	})
}
