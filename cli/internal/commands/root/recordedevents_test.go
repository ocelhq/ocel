package root

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/pkg/processenv"
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

func spooledEventNames(t *testing.T) []string {
	t.Helper()
	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	events, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range events {
		var event telemetry.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		names = append(names, event.Name)
	}
	return names
}

func TestAnInitOutsideDebugModeSpoolsInitCompletedThenCommandCompletedAndStartsOneFlushAfterBoth(t *testing.T) {
	withTelemetryBuild(t)
	withBannerShown(t)
	t.Chdir(t.TempDir())
	ocel := newCommand()
	ocel.root.SetOut(io.Discard)
	ocel.root.SetErr(io.Discard)
	var spooledAtStart [][]string
	ocel.startFlush = func(telemetry.Resolution) { spooledAtStart = append(spooledAtStart, spooledEventNames(t)) }

	code := ocel.executeAndReport([]string{"init", "--provider", "fake"})

	want := []string{"init_completed", "command_completed"}
	if got := spooledEventNames(t); code != 0 || !slices.Equal(got, want) {
		t.Errorf("code = %d, spooled events = %v, want %v", code, got, want)
	}
	if len(spooledAtStart) != 1 || !slices.Equal(spooledAtStart[0], want) {
		t.Errorf("spooled events at each flush start = %v, want one start after %v", spooledAtStart, want)
	}
}

func TestAnInitUnderJSONPrintsItsEventsOnStderrAndOnlyItsResultOnStdout(t *testing.T) {
	debugTelemetry(t)
	t.Chdir(t.TempDir())

	code, stdout, stderr := executeAndReportRoot(t, "--json", "init", "--provider", "fake")

	events := telemetryEvents(t, stderr)
	if code != 0 || len(events) != 2 || events[0].Name != "init_completed" {
		t.Fatalf("code = %d, events = %+v, want init_completed then command_completed on stderr", code, events)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("stdout %q is not a JSON document: %v", stdout, err)
	}
	if decoder.More() || strings.Contains(stdout, telemetry.DebugPrefix) {
		t.Errorf("stdout = %q, want the init result alone", stdout)
	}
}

func TestAnInitFromABuildWithoutATelemetryEndpointSpoolsNothing(t *testing.T) {
	withTelemetryBuild(t)
	withBannerShown(t)
	withTelemetryEndpoint(t, "")
	t.Chdir(t.TempDir())

	code, _, _ := executeAndReportRoot(t, "init", "--provider", "fake")

	if got := spooledEventNames(t); code != 0 || len(got) != 0 {
		t.Errorf("code = %d, spooled events = %v, want nothing from a build without an endpoint", code, got)
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

func declareRealtimeScript(name string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.%s), {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.%s },
    body: JSON.stringify({ resource: { type: "RESOURCE_TYPE_REALTIME", name: %q }, realtime: {} }),
  }),
);
export {};
`, processenv.DevServerEnvVar, processenv.DevServerTokenEnvVar, name)
}

func waitForFileContaining(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(path); err == nil && strings.Contains(string(content), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never contained %q", path, want)
}

func TestAnInterruptedDevSessionPrintsADevSessionEndedEventWithItsDeclaredKindsBeforeItsCommandCompletedEvent(t *testing.T) {
	debugTelemetry(t)
	root := filepath.Join(t.TempDir(), "my-secret-project")
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), declareRealtimeScript("mysecretfeed"))
	t.Chdir(root)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	var stdout, stderr lockedBuffer
	ocel := newCommand()
	ocel.root.SetContext(ctx)
	ocel.root.SetOut(&stdout)
	ocel.root.SetErr(&stderr)
	envDumpPath := filepath.Join(root, "env.out")
	done := make(chan int, 1)
	go func() {
		done <- ocel.executeAndReport([]string{"dev", "--", "sh", "-c", "env > " + envDumpPath + "; sleep 30"})
	}()
	waitForFileContaining(t, envDumpPath, "OCEL_RESOURCE_REALTIME_mysecretfeed=")

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
	if kinds, _ := session["resource_kinds"].([]any); len(kinds) != 1 || kinds[0] != "realtime" {
		t.Errorf("resource_kinds = %v, want [realtime]", session["resource_kinds"])
	}
	if session["reloads"] != float64(0) || len(session["error_codes"].(map[string]any)) != 0 {
		t.Errorf("properties = %v, want no reloads or error codes", session)
	}
	if duration, _ := session["duration_ms"].(float64); duration <= 0 {
		t.Errorf("duration_ms = %v, want a positive number", session["duration_ms"])
	}
	if events[1].Properties["error_code"] != "interrupted" {
		t.Errorf("command_completed error_code = %v, want interrupted", events[1].Properties["error_code"])
	}
	for _, line := range strings.Split(stderr.String(), "\n") {
		if !strings.HasPrefix(line, telemetry.DebugPrefix) {
			continue
		}
		for _, leaked := range []string{"secret", root, os.TempDir()} {
			if strings.Contains(line, leaked) {
				t.Errorf("event %s contains %q", line, leaked)
			}
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
