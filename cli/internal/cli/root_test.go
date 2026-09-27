package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/runui"
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

func executeRoot(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	origFormat := logFormatFlag
	var out, errOut bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	t.Cleanup(func() {
		logFormatFlag = origFormat
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	if err := Execute(); err != nil {
		t.Fatalf("ocel %s: %v; stdout=%s stderr=%s", strings.Join(args, " "), err, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func inDeployFixture(t *testing.T) {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	t.Chdir(root)
}

func TestLogFormatJSONAttachesOnlyTheJSONSink(t *testing.T) {
	inDeployFixture(t)

	stdout, stderr := executeRoot(t, "--log-format", "json", "deployments", "prune")

	var ended bool
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not JSON, want every line from the one JSON sink: %v", line, err)
		}
		_, ended = ev["result"]
	}
	if !ended {
		t.Errorf("stdout = %q, want the run's events ending in its result", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing beside the one JSON stream", stderr)
	}
}

func TestTheHumanLogFormatAttachesOnlyTheHumanSink(t *testing.T) {
	inDeployFixture(t)

	stdout, _ := executeRoot(t, "deployments", "prune")

	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if json.Valid([]byte(line)) {
			t.Fatalf("line %q is JSON, want only the human view", line)
		}
	}
	if !strings.Contains(stdout, "Pruned") {
		t.Errorf("stdout = %q, want the run's headline in the human view", stdout)
	}
}

func TestACommandWhoseStdoutIsItsDataDrawsItsRunOnStderr(t *testing.T) {
	inDeployFixture(t)

	stdout, stderr := executeRoot(t, "--log-format", "json", "deployments", "ls")

	if !strings.Contains(stdout, "promo-2") || strings.Contains(stdout, "{") {
		t.Errorf("stdout = %q, want the promotions table alone", stdout)
	}
	if evs := runEvents(t, stderr); len(evs) == 0 || !evs[len(evs)-1].GetResult().GetSuccess() {
		t.Errorf("stderr = %q, want the run's events ending in its result", stderr)
	}
}

func TestEveryCommandWhoseStdoutIsItsDataIsMarkedToDrawItsRunOnStderr(t *testing.T) {
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

func TestEveryCommandThatReportsThroughItsRunIsMarkedToDrawItOnStdout(t *testing.T) {
	for _, path := range [][]string{{"deploy"}, {"domain", "use"}, {"domain", "add"}, {"connector", "add"}, {"connector", "rm"}, {"deployments", "prune"}} {
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
