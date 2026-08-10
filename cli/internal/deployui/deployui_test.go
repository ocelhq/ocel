package deployui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func newTestSession(t *testing.T, command string) (*Session, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	var out bytes.Buffer
	s := New(&out, dir, command, false)
	t.Cleanup(func() { _ = s.Close() })
	return s, &out, filepath.Join(dir, ".ocel", "logs", "ocel.log")
}

func progress(phase deploymentsv1.Phase, msg string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{Event: &deploymentsv1.DeployEvent_Progress{
		Progress: &deploymentsv1.ProgressEvent{Phase: phase, Message: msg},
	}}
}

func progressN(phase deploymentsv1.Phase, msg string, current, total uint32) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{Event: &deploymentsv1.DeployEvent_Progress{
		Progress: &deploymentsv1.ProgressEvent{Phase: phase, Message: msg, Current: &current, Total: &total},
	}}
}

func TestSession_RawMode_StreamsEventsAndWritesLog(t *testing.T) {
	s, out, logPath := newTestSession(t, "ocel deploy")

	s.Building()
	s.Event(progress(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts"))
	s.Event(&deploymentsv1.DeployEvent{Event: &deploymentsv1.DeployEvent_Log{
		Log: &deploymentsv1.LogEvent{Message: "pulumi engine line"},
	}})
	s.Deployed("Deployed", []string{"https://app.example.workers.dev"}, nil)

	got := out.String()
	for _, want := range []string{
		"Uploading function artifacts",
		"pulumi engine line",
		"Deployed in",
		"https://app.example.workers.dev",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(logBytes)
	for _, want := range []string{"[building]", "[uploading]", "[log] pulumi engine line"} {
		if !strings.Contains(log, want) {
			t.Errorf("log = %q, want it to contain %q", log, want)
		}
	}
}

func TestSession_DeterminateProgress_LoggedWithCounts(t *testing.T) {
	s, _, logPath := newTestSession(t, "ocel deploy")
	s.Event(progressN(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", 3, 5))
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(log), "(3/5)") {
		t.Errorf("log = %q, want it to record the 3/5 count", log)
	}
}

func TestSession_Fail_RendersErrorAndLogPointer(t *testing.T) {
	s, out, _ := newTestSession(t, "ocel deploy")
	s.Building()
	s.Fail(errors.New("creating rds: InsufficientCapacity"))

	got := out.String()
	if !strings.Contains(got, "creating rds: InsufficientCapacity") {
		t.Errorf("stdout = %q, want the error message", got)
	}
	if !strings.Contains(got, "ocel.log") {
		t.Errorf("stdout = %q, want a pointer to the log file", got)
	}
}

func TestSession_Cancel_WarnsPartialStateAndReconcileHint(t *testing.T) {
	s, out, _ := newTestSession(t, "ocel deploy")
	s.Event(progress(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources"))
	s.Cancel()

	got := out.String()
	for _, want := range []string{"Cancelled", "partially created", "ocel deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
}

func TestBar(t *testing.T) {
	cases := []struct {
		current, total uint32
		wantFilled     int
	}{
		{0, 5, 0},
		{5, 5, barWidth},
		{10, 5, barWidth},
	}
	for _, tc := range cases {
		got := bar(tc.current, tc.total)
		if filled := strings.Count(got, "█"); filled != tc.wantFilled {
			t.Errorf("bar(%d,%d) filled = %d, want %d", tc.current, tc.total, filled, tc.wantFilled)
		}
	}
}

func TestStepIdentity(t *testing.T) {
	k1, title1 := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack")
	k2, _ := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources")
	if k1 != k2 {
		t.Errorf("same-phase messages produced different keys %q vs %q", k1, k2)
	}
	if title1 != "Provisioning" {
		t.Errorf("title = %q, want Provisioning", title1)
	}
	k3, title3 := stepIdentity(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase")
	if k3 == k1 || title3 != "Ensuring passphrase" {
		t.Errorf("unspecified step identity = (%q,%q), want its own message-keyed step", k3, title3)
	}
}

func TestWaiting_PrintsWhereToGoAndHowToAbort(t *testing.T) {
	s, out, logPath := newTestSession(t, "ocel deploy")
	s.Waiting("1 variable is not ready — nothing has been built.\n\n  STRIPE_API_KEY (project root)\n", "http://127.0.0.1:5555/#t=abc")

	got := out.String()
	for _, want := range []string{"STRIPE_API_KEY", "http://127.0.0.1:5555/#t=abc", "Ctrl-C"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to contain %q", got, want)
		}
	}
	if raw, err := os.ReadFile(logPath); err == nil && !strings.Contains(string(raw), "waiting") {
		t.Errorf("log = %s, want the wait recorded", raw)
	}
}

func TestCancel_WhileWaiting_DoesNotWarnAboutResourcesThatCannotExist(t *testing.T) {
	s, out, _ := newTestSession(t, "ocel deploy")
	s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
	s.Cancel()

	got := out.String()
	if strings.Contains(got, "Resources may be partially created") {
		t.Errorf("stdout = %q, want no partial-provisioning warning for a run cancelled before provisioning", got)
	}
	if !strings.Contains(got, "Nothing has been provisioned") {
		t.Errorf("stdout = %q, want the cancel to say nothing was provisioned", got)
	}
}

func TestWaiting_NeverPersistsTheSessionTokenToTheLog(t *testing.T) {
	const token = "s3cr3t-session-token"
	s, out, logPath := newTestSession(t, "ocel deploy")
	s.Waiting("1 variable is not ready.", "http://127.0.0.1:41234/#t="+token)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(raw)
	if strings.Contains(log, token) {
		t.Errorf("log = %q, want the session token never persisted", log)
	}
	if !strings.Contains(log, "[waiting] http://127.0.0.1:41234/") {
		t.Errorf("log = %q, want the wait recorded with the address", log)
	}
	if !strings.Contains(out.String(), token) {
		t.Errorf("stdout = %q, want the full URL on screen", out.String())
	}
}

type lockedWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func newCleanTestSession(t *testing.T) (*Session, *lockedWriter) {
	t.Helper()
	prev := isTTY
	isTTY = func(io.Writer) bool { return true }
	t.Cleanup(func() { isTTY = prev })

	out := &lockedWriter{}
	s := New(out, t.TempDir(), "ocel deploy", false)
	t.Cleanup(func() { _ = s.Close() })
	if !s.clean {
		t.Fatal("session is not in the phased view, so the render loop is not running")
	}
	return s, out
}

func TestWaiting_StopsTheRenderLoopSoTheBlockSurvives(t *testing.T) {
	s, out := newCleanTestSession(t)
	s.Building()
	time.Sleep(3 * frameRate)

	s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
	settled := out.String()
	time.Sleep(5 * frameRate)

	if got := out.String(); got != settled {
		t.Errorf("the render loop painted %q over the waiting block", strings.TrimPrefix(got, settled))
	}
}

func TestResume_RestartsTheSpinnerTheWaitStopped(t *testing.T) {
	s, out := newCleanTestSession(t)
	s.Building()
	s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
	quiet := out.String()

	s.Resume()
	time.Sleep(5 * frameRate)

	if out.String() == quiet {
		t.Error("nothing was painted after Resume, so the paused step never restarted")
	}
}

func TestCancel_AfterResume_WarnsAboutResourcesAgain(t *testing.T) {
	s, out, _ := newTestSession(t, "ocel deploy")
	s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
	s.Resume()
	s.Cancel()

	got := out.String()
	if !strings.Contains(got, "Resources may be partially created") {
		t.Errorf("stdout = %q, want a resumed run cancelled later to warn about partial provisioning", got)
	}
}
