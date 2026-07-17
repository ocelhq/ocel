package deployui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// A bytes.Buffer is not a *os.File, so New treats it as non-TTY and renders in
// raw mode: every event is streamed to stdout and mirrored to the log.
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
		{10, 5, barWidth}, // clamp when current exceeds total
	}
	for _, tc := range cases {
		got := bar(tc.current, tc.total)
		if filled := strings.Count(got, "█"); filled != tc.wantFilled {
			t.Errorf("bar(%d,%d) filled = %d, want %d", tc.current, tc.total, filled, tc.wantFilled)
		}
	}
}

func TestStepIdentity(t *testing.T) {
	// A typed phase groups its messages under the phase.
	k1, title1 := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack")
	k2, _ := stepIdentity(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources")
	if k1 != k2 {
		t.Errorf("same-phase messages produced different keys %q vs %q", k1, k2)
	}
	if title1 != "Provisioning" {
		t.Errorf("title = %q, want Provisioning", title1)
	}
	// An unclassified event is its own step titled by its message.
	k3, title3 := stepIdentity(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase")
	if k3 == k1 || title3 != "Ensuring passphrase" {
		t.Errorf("unspecified step identity = (%q,%q), want its own message-keyed step", k3, title3)
	}
}
