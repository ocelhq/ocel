package deployui

import (
	"bytes"
	"errors"
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
	out := &lockedWriter{}
	s := newSession(out, t.TempDir(), "ocel deploy", true)
	t.Cleanup(func() { _ = s.Close() })
	if !s.clean {
		t.Fatal("session is not in the phased view, so the render loop is not running")
	}
	return s, out
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(raw)
}

func TestSession(t *testing.T) {
	t.Run("waiting stops the render loop so the block survives", func(t *testing.T) {
		s, out := newCleanTestSession(t)
		s.Building()
		time.Sleep(3 * frameRate)

		s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
		settled := out.String()
		time.Sleep(5 * frameRate)

		if got := out.String(); got != settled {
			t.Errorf("the render loop painted %q over the waiting block", strings.TrimPrefix(got, settled))
		}
	})

	t.Run("resume restarts the spinner the wait stopped", func(t *testing.T) {
		s, out := newCleanTestSession(t)
		s.Building()
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
		quiet := out.String()

		s.Resume()
		time.Sleep(5 * frameRate)

		if out.String() == quiet {
			t.Error("nothing was painted after Resume, so the paused step never restarted")
		}
	})

	t.Run("raw mode streams events and writes the log", func(t *testing.T) {
		t.Parallel()
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
		log := readLog(t, logPath)
		for _, want := range []string{"[building]", "[uploading]", "[log] pulumi engine line"} {
			if !strings.Contains(log, want) {
				t.Errorf("log = %q, want it to contain %q", log, want)
			}
		}
	})

	t.Run("determinate progress is logged with counts", func(t *testing.T) {
		t.Parallel()
		s, _, logPath := newTestSession(t, "ocel deploy")
		s.Event(progressN(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", 3, 5))
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		if log := readLog(t, logPath); !strings.Contains(log, "(3/5)") {
			t.Errorf("log = %q, want it to record the 3/5 count", log)
		}
	})

	t.Run("fail renders the error and a log pointer", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("cancel warns about partial state and hints at reconciling", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Event(progress(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources"))
		s.Cancel()

		got := out.String()
		for _, want := range []string{"Cancelled", "partially created", "ocel deploy"} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("waiting prints where to go and how to abort", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("cancel while waiting does not warn about resources that cannot exist", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("waiting never persists the session token to the log", func(t *testing.T) {
		t.Parallel()
		const token = "s3cr3t-session-token"
		s, out, logPath := newTestSession(t, "ocel deploy")
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:41234/#t="+token)
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		log := readLog(t, logPath)
		if strings.Contains(log, token) {
			t.Errorf("log = %q, want the session token never persisted", log)
		}
		if !strings.Contains(log, "[waiting] http://127.0.0.1:41234/") {
			t.Errorf("log = %q, want the wait recorded with the address", log)
		}
		if !strings.Contains(out.String(), token) {
			t.Errorf("stdout = %q, want the full URL on screen", out.String())
		}
	})

	t.Run("cancel after resume warns about resources again", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
		s.Resume()
		s.Cancel()

		got := out.String()
		if !strings.Contains(got, "Resources may be partially created") {
			t.Errorf("stdout = %q, want a resumed run cancelled later to warn about partial provisioning", got)
		}
	})
}

func TestBar(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		current, total uint32
		wantFilled     int
	}{
		{"is empty at zero", 0, 5, 0},
		{"is full at the total", 5, 5, barWidth},
		{"stays full past the total", 10, 5, barWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bar(tc.current, tc.total)
			if filled := strings.Count(got, "█"); filled != tc.wantFilled {
				t.Errorf("bar(%d,%d) filled = %d, want %d", tc.current, tc.total, filled, tc.wantFilled)
			}
		})
	}
}

func TestStepIdentity(t *testing.T) {
	t.Parallel()

	provisioning, title := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack")

	t.Run("keys a step by its phase, not its message", func(t *testing.T) {
		t.Parallel()
		other, _ := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources")
		if provisioning != other {
			t.Errorf("same-phase messages produced different keys %q vs %q", provisioning, other)
		}
		if title != "Provisioning" {
			t.Errorf("title = %q, want Provisioning", title)
		}
	})

	t.Run("gives an unspecified phase its own message-keyed step", func(t *testing.T) {
		t.Parallel()
		key, unspecified := stepIdentity(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase")
		if key == provisioning || unspecified != "Ensuring passphrase" {
			t.Errorf("unspecified step identity = (%q,%q), want its own message-keyed step", key, unspecified)
		}
	})
}
