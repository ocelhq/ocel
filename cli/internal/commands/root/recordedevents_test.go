package root

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
