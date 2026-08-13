package deployui

import (
	"bytes"
	"fmt"
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
		r.Progress(appA, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 2, u32(2)) // app-a finishes

		if len(r.plan.activeOrder) != 1 || r.plan.activeOrder[0] != stageKey(appB) {
			t.Fatalf("activeOrder = %v, want only app-b still live", r.plan.activeOrder)
		}
		if got := out.String(); !strings.Contains(got, "app-a") {
			t.Errorf("output = %q, want app-a's finished line committed to scrollback", got)
		}

		r.Progress(appB, deploymentsv1.Phase_PHASE_UPLOADING, "uploading", 2, u32(2))
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
	r.Deployed("Deployed", []string{"https://app.example.workers.dev"}, "")

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
		{400 * time.Millisecond, "1s"},
		{1100 * time.Millisecond, "1s"},
		{1600 * time.Millisecond, "2s"},
		{59*time.Second + 600*time.Millisecond, "1m00s"},
		{90 * time.Second, "1m30s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRestartLegacyStageDiscardsElapsedTime(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newRendererForTest(&out, FormatHuman, true, false)
	t.Cleanup(func() { _ = r.Close() })

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	r.useClock(func() time.Time { return time.Unix(0, nowNanos.Load()) })

	r.Building()

	nowNanos.Add(int64(90 * time.Second))
	r.RestartLegacyStage(deploymentsv1.Phase_PHASE_BUILDING)

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
