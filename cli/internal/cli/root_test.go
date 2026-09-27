package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/runui"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestTheRootFlagsFeedTheOneResolver(t *testing.T) {
	origFormat, origVerbose := logFormatFlag, verboseFlag
	t.Cleanup(func() { logFormatFlag, verboseFlag = origFormat, origVerbose })

	logFormatFlag, verboseFlag = string(runui.FormatJSON), true

	p := presentation(&bytes.Buffer{})
	if p.Format != runui.FormatJSON {
		t.Errorf("Format = %q, want the --log-format flag to reach the resolver", p.Format)
	}
	if !p.Verbose {
		t.Errorf("Verbose = false, want the --verbose flag to reach the resolver")
	}
}

func newTestDeps() cmddeps.Deps {
	deps := newDeps()
	deps.Events = events.NewBus(time.Now)
	return deps
}

func shownRun(t *testing.T, format runui.Format) []string {
	t.Helper()
	origFormat := logFormatFlag
	t.Cleanup(func() { logFormatFlag = origFormat })
	logFormatFlag = string(format)

	var out bytes.Buffer
	deps := cmddeps.Deps{Events: events.NewBus(time.Now), Presentation: presentation}
	deps.AttachTerminalSink(&out)

	_, run, err := deps.Events.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	run.Phase(progressv1.Phase_PHASE_CHECK).Warn("the edge plan is unknown")
	run.End(&err)
	if err := deps.Events.Close(); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(out.String()), "\n")
}

func TestLogFormatJSONAttachesOnlyTheJSONSink(t *testing.T) {
	lines := shownRun(t, runui.FormatJSON)

	var said bool
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not JSON, want every line from the one JSON sink: %v", line, err)
		}
		said = said || ev["message"] == "the edge plan is unknown"
	}
	if !said {
		t.Errorf("lines = %q, want the run's message as a JSON event", lines)
	}
}

func TestTheHumanLogFormatAttachesOnlyTheHumanSink(t *testing.T) {
	lines := shownRun(t, runui.FormatHuman)

	var said bool
	for _, line := range lines {
		if json.Valid([]byte(line)) {
			t.Fatalf("line %q is JSON, want only the human view", line)
		}
		said = said || strings.Contains(line, "the edge plan is unknown")
	}
	if !said {
		t.Errorf("lines = %q, want the run's message in the human view", lines)
	}
}

func TestACommandWhoseStdoutIsItsDataDrawsItsRunOnStderr(t *testing.T) {
	for _, path := range [][]string{
		{"bindings", "ls"}, {"bindings", "set"}, {"bindings", "rm"}, {"bindings", "generate"},
		{"env", "ls"}, {"env", "get"}, {"env", "set"}, {"env", "ui"},
		{"cost", "scan"},
		{"domain", "ls"}, {"domain", "status"},
		{"preview", "ls"},
		{"deployments", "ls"},
		{"permissions"},
		{"connector", "status"},
		{"doctor"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		t.Cleanup(func() { cmd.SetOut(nil); cmd.SetErr(nil) })
		if got := cmddeps.ChooseRunOutput(cmd); got != &stderr {
			t.Errorf("ocel %s draws its run on stdout, want stderr so stdout holds only its data", strings.Join(path, " "))
		}
	}
}

func TestACommandThatReportsThroughItsRunDrawsItOnStdout(t *testing.T) {
	for _, path := range [][]string{{"deploy"}, {"domain", "use"}, {"domain", "add"}, {"connector", "add"}, {"connector", "rm"}} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		t.Cleanup(func() { cmd.SetOut(nil); cmd.SetErr(nil) })
		if got := cmddeps.ChooseRunOutput(cmd); got != &stdout {
			t.Errorf("ocel %s draws its run on stderr, want stdout", strings.Join(path, " "))
		}
	}
}
