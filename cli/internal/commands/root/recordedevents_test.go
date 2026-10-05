package root

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
)

func TestASuccessfulInitPrintsAnInitCompletedEventBeforeItsCommandCompletedEvent(t *testing.T) {
	debugTelemetry(t)
	dir := filepath.Join(t.TempDir(), "my-secret-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	code, _, stderr := executeAndReportRoot(t, "init", "my-secret-slug", "--provider", "fake", "--yaml")

	events := telemetryEvents(t, stderr)
	if code != 0 || len(events) != 2 || events[0].Name != "init_completed" || events[1].Name != "command_completed" {
		t.Fatalf("code = %d, events = %+v, want init_completed then command_completed", code, events)
	}
	want := map[string]any{"language": "", "package_manager": "", "provider": "fake", "config_format": "yaml"}
	for name, value := range want {
		if events[0].Properties[name] != value {
			t.Errorf("property %s = %#v, want %#v", name, events[0].Properties[name], value)
		}
	}
}

func TestTheInitCompletedEventNamesNeitherTheProjectNorAPath(t *testing.T) {
	debugTelemetry(t)
	dir := filepath.Join(t.TempDir(), "my-secret-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	_, _, stderr := executeAndReportRoot(t, "init", "my-secret-slug", "--provider", "fake")

	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "[telemetry] ") {
			continue
		}
		for _, leaked := range []string{"secret", dir, os.TempDir()} {
			if strings.Contains(line, leaked) {
				t.Errorf("event %s contains %q", line, leaked)
			}
		}
	}
}

func TestAnInitThatFailsPrintsNoInitCompletedEvent(t *testing.T) {
	debugTelemetry(t)
	t.Chdir(t.TempDir())

	code, _, stderr := executeAndReportRoot(t, "init", "--provider", "no-such-provider")

	events := telemetryEvents(t, stderr)
	if code == 0 || len(events) != 1 || events[0].Name != "command_completed" {
		t.Errorf("code = %d, events = %+v, want only command_completed", code, events)
	}
}

func TestAnInitWithTelemetryOffPrintsNoEvent(t *testing.T) {
	t.Setenv("OCEL_TELEMETRY", "0")
	t.Chdir(t.TempDir())

	_, _, stderr := executeAndReportRoot(t, "init", "--provider", "fake")

	if strings.Contains(stderr, "[telemetry]") {
		t.Errorf("stderr = %q, want no event", stderr)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAnInterruptedDevSessionPrintsADevSessionEndedEventBeforeItsCommandCompletedEvent(t *testing.T) {
	debugTelemetry(t)
	root := t.TempDir()
	t.Chdir(root)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	var stdout, stderr lockedBuffer
	ocel := newCommand()
	ocel.root.SetContext(ctx)
	ocel.root.SetOut(&stdout)
	ocel.root.SetErr(&stderr)
	done := make(chan int, 1)
	go func() { done <- ocel.executeAndReport([]string{"dev", "--", "sleep", "30"}) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, found, _ := leader.Find(root); found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	interrupt()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ocel dev did not exit after the interrupt")
	}
	events := telemetryEvents(t, stderr.String())
	if len(events) != 2 || events[0].Name != "dev_session_ended" || events[1].Name != "command_completed" {
		t.Fatalf("events = %+v, want dev_session_ended then command_completed", events)
	}
	session := events[0].Properties
	if session["reloads"] != float64(0) || len(session["resource_kinds"].([]any)) != 0 || len(session["error_codes"].(map[string]any)) != 0 {
		t.Errorf("properties = %v, want no reloads, kinds or error codes", session)
	}
	if duration, _ := session["duration_ms"].(float64); duration <= 0 {
		t.Errorf("duration_ms = %v, want a positive number", session["duration_ms"])
	}
	if events[1].Properties["error_code"] != "interrupted" {
		t.Errorf("command_completed error_code = %v, want interrupted", events[1].Properties["error_code"])
	}
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(line, "[telemetry] ") && strings.Contains(line, root) {
			t.Errorf("event %s names the project path", line)
		}
	}
}

func TestADevSessionWithTelemetryOffPrintsNoEvent(t *testing.T) {
	t.Setenv("OCEL_TELEMETRY", "0")
	t.Chdir(t.TempDir())

	_, _, stderr := executeAndReportRoot(t, "dev", "--", "sh", "-c", "exit 0")

	if strings.Contains(stderr, "[telemetry]") {
		t.Errorf("stderr = %q, want no event", stderr)
	}
}
