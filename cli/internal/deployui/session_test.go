package deployui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/obs"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func startTestRun(t *testing.T, dir, command string) *obs.Run {
	t.Helper()
	_, run, err := obs.Start(context.Background(), dir, command)
	if err != nil {
		t.Fatalf("obs.Start() = %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return run
}

func newTestSession(t *testing.T, command string) (*Session, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	run := startTestRun(t, dir, command)
	var out bytes.Buffer
	s := New(&out, run, FormatHuman, false)
	t.Cleanup(func() { _ = s.Close() })
	return s, &out, s.LogPath()
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

func stageProgress(stageID []byte, msg string, current, total uint32) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{Event: &deploymentsv1.DeployEvent_Progress{
		Progress: &deploymentsv1.ProgressEvent{StageId: stageID, Message: msg, Current: &current, Total: &total},
	}}
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
		if !strings.Contains(got, ".log") {
			t.Errorf("stdout = %q, want a pointer to the log file", got)
		}
	})

	t.Run("fail with no active step still prints a failure line", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Fail(errors.New("boom"))

		if !strings.Contains(out.String(), "Failed") {
			t.Errorf("stdout = %q, want a bare Failed line", out.String())
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

	t.Run("a stage plan event is dispatched to the renderer's tree", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newTestSession(t, "ocel deploy")

		build := []byte{1, 0, 0, 0, 0, 0, 0, 0}
		s.Event(&deploymentsv1.DeployEvent{Event: &deploymentsv1.DeployEvent_StagePlan{
			StagePlan: &deploymentsv1.StagePlanEvent{
				Stages: []*deploymentsv1.Stage{{Id: build, Title: "Building"}},
				Final:  true,
			},
		}})

		if title := s.r.plan.nodes[stageKey(build)].title; title != "Building" {
			t.Errorf("stage title = %q, want %q", title, "Building")
		}
		if !s.r.plan.final {
			t.Error("plan.final = false after a StagePlanEvent with Final: true")
		}
	})

	t.Run("a child stage arriving before its parent still attaches", func(t *testing.T) {
		t.Parallel()
		plan := newStagePlan()
		parent := []byte{9, 0, 0, 0, 0, 0, 0, 0}
		child := []byte{10, 0, 0, 0, 0, 0, 0, 0}

		plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
			{Id: child, ParentId: parent, Title: "app-a"},
		}})
		if _, ok := plan.nodes[stageKey(child)]; !ok {
			t.Fatal("orphan child was not recorded at all")
		}
		if plan.nodes[stageKey(child)].linked {
			t.Error("orphan child linked before its parent arrived")
		}

		plan.apply(&deploymentsv1.StagePlanEvent{Stages: []*deploymentsv1.Stage{
			{Id: parent, Title: "apps"},
		}})

		parentNode := plan.nodes[stageKey(parent)]
		if len(parentNode.children) != 1 || parentNode.children[0] != stageKey(child) {
			t.Errorf("parent.children = %v, want the orphan attached", parentNode.children)
		}
		if !plan.nodes[stageKey(child)].linked {
			t.Error("child was not marked linked once its parent arrived")
		}
	})

	t.Run("each run gets its own log file and old ones are pruned, not truncated", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		var paths []string
		for i := 0; i < 12; i++ {
			_, run, err := obs.Start(context.Background(), dir, "ocel deploy")
			if err != nil {
				t.Fatalf("obs.Start() = %v", err)
			}
			var out bytes.Buffer
			s := New(&out, run, FormatHuman, false)
			s.Building()
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			if err := run.Close(); err != nil {
				t.Fatalf("run.Close() = %v", err)
			}
			paths = append(paths, s.LogPath())
		}

		for i, p := range paths {
			if i < 2 {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("oldest run %d log %s should have been pruned, stat err = %v", i, p, err)
				}
				continue
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("run %d log %s should have survived pruning: %v", i, p, err)
			}
		}
		for i := 1; i < len(paths); i++ {
			if paths[i] == paths[i-1] {
				t.Fatalf("two runs shared a log file path: %s", paths[i])
			}
		}
	})
}

func TestFormatAxis(t *testing.T) {
	t.Run("json format emits one machine-readable line per event, never the human text", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatJSON, false)
		t.Cleanup(func() { _ = s.Close() })

		s.Building()
		s.Event(progress(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading function artifacts"))
		s.Deployed("Deployed", []string{"https://app.example.workers.dev"}, nil)

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("got %d stdout lines, want 3 (building, progress, deployed): %q", len(lines), out.String())
		}
		for _, line := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("line %q is not valid JSON: %v", line, err)
			}
			if _, ok := rec["type"]; !ok {
				t.Errorf("line %q has no %q field", line, "type")
			}
		}
		if strings.Contains(lines[1], "\r") || strings.HasPrefix(lines[1], "Uploading") {
			t.Errorf("progress line %q looks like the raw human line, not a JSON record", lines[1])
		}
	})

	t.Run("verbose does not change the output format", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatJSON, true)
		t.Cleanup(func() { _ = s.Close() })

		s.Event(progress(deploymentsv1.Phase_PHASE_BUILDING, "Building"))

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
			t.Fatalf("verbose=true changed the json format away from valid JSON: %v (stdout = %q)", err, out.String())
		}
	})

	t.Run("json format never enters the live-region view", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		s := New(&bytes.Buffer{}, run, FormatJSON, false)
		t.Cleanup(func() { _ = s.Close() })
		if s.r.Live() {
			t.Error("json format entered the live-region view, which only makes sense for human output on a terminal")
		}
	})

	t.Run("default format is human-readable, independent of verbosity", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatHuman, true)
		t.Cleanup(func() { _ = s.Close() })

		s.Event(progress(deploymentsv1.Phase_PHASE_BUILDING, "Building project"))

		if !strings.Contains(out.String(), "Building project") {
			t.Errorf("stdout = %q, want the human-readable line", out.String())
		}
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err == nil {
			t.Errorf("stdout = %q, want human text, not JSON, when format is human even under verbose", out.String())
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

func TestLegacyStageIdentity(t *testing.T) {
	t.Parallel()

	provisioning := legacyStageID(deploymentsv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack")
	title := legacyStageTitle(deploymentsv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack")

	t.Run("keys a step by its phase, not its message", func(t *testing.T) {
		t.Parallel()
		other := legacyStageID(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning resources")
		if provisioning != other {
			t.Errorf("same-phase messages produced different keys %q vs %q", provisioning, other)
		}
		if title != "Provisioning" {
			t.Errorf("title = %q, want Provisioning", title)
		}
	})

	t.Run("gives an unspecified phase its own message-keyed step", func(t *testing.T) {
		t.Parallel()
		key := legacyStageID(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase")
		unspecified := legacyStageTitle(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase")
		if key == provisioning || unspecified != "Ensuring passphrase" {
			t.Errorf("unspecified step identity = (%q,%q), want its own message-keyed step", key, unspecified)
		}
	})
}
