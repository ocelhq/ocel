package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

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
	var out, errOut bytes.Buffer
	executeRootOn(t, &out, &errOut, args...)
	return out.String(), errOut.String()
}

func executeRootOn(t *testing.T, stdout, stderr io.Writer, args ...string) {
	t.Helper()
	origFormat := logFormatFlag
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	t.Cleanup(func() {
		logFormatFlag = origFormat
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	if err := Execute(); err != nil {
		t.Fatalf("ocel %s: %v", strings.Join(args, " "), err)
	}
}

func aTerminal(t *testing.T, term string, columns uint16) (tty *os.File, screen func() string) {
	t.Helper()
	t.Setenv("TERM", term)
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: columns}); err != nil {
		t.Fatalf("size the pty: %v", err)
	}
	var got bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&got, ptmx)
		close(drained)
	}()
	return tty, func() string {
		tty.Close()
		<-drained
		return got.String()
	}
}

const liveFrame = "\x1b[?2026h"

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

func TestTheHumanLogFormatAttachesOnlyTheGroupedSink(t *testing.T) {
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

func TestATerminalFortyColumnsWideGetsTheLiveLineView(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 40)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune")

	if got := screen(); !strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the transcript drawn in live-line frames", got)
	}
}

func TestADumbTerminalGetsTheGroupedView(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "dumb", 80)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestATerminalNarrowerThanFortyColumnsGetsTheGroupedView(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 39)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestATerminalThatReportsNoWidthGetsTheGroupedViewWhateverColumnsSays(t *testing.T) {
	inDeployFixture(t)
	t.Setenv("COLUMNS", "120")
	tty, screen := aTerminal(t, "xterm-256color", 0)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestAPipedStdoutGetsTheGroupedViewEvenWithATerminalOnStderr(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)
	var stdout bytes.Buffer

	executeRootOn(t, &stdout, tty, "deployments", "prune")

	if got := stdout.String(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("stdout = %q, want the grouped transcript with no live line", got)
	}
	if got := screen(); got != "" {
		t.Errorf("the terminal on stderr shows %q, want nothing: the run draws on stdout", got)
	}
}

func TestACommandWhoseStdoutIsItsDataDrawsTheLiveLineOnAStderrTerminal(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)
	var stdout bytes.Buffer

	executeRootOn(t, &stdout, tty, "deployments", "ls")

	if got := stdout.String(); !strings.Contains(got, "promo-2") || strings.Contains(got, liveFrame) {
		t.Errorf("stdout = %q, want the promotions table alone", got)
	}
	if got := screen(); !strings.Contains(got, liveFrame) {
		t.Errorf("the terminal on stderr shows %q, want the run drawn in live-line frames", got)
	}
}

func TestACommandWhoseStdoutIsItsDataDrawsTheGroupedViewOnAPipedStderr(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)
	var stderr bytes.Buffer

	executeRootOn(t, tty, &stderr, "deployments", "ls")

	if got := stderr.String(); got == "" || strings.Contains(got, liveFrame) {
		t.Errorf("stderr = %q, want the grouped view of the run", got)
	}
	if got := screen(); !strings.Contains(got, "promo-2") || strings.Contains(got, liveFrame) {
		t.Errorf("the terminal on stdout shows %q, want the promotions table alone", got)
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

func TestACommandWhoseDataAndRunShareOneTerminalDrawsTheGroupedViewSoNoLineOfDataLandsOnTheLiveRow(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)

	executeRootOn(t, tty, tty, "deployments", "ls")

	got := screen()
	if strings.Contains(got, liveFrame) {
		t.Errorf("the terminal shows %q, want no live line while the command's data shares its terminal", got)
	}
	if !strings.Contains(ansi.Strip(got), "\nID  ") {
		t.Errorf("the terminal shows %q, want the promotions table header on a row of its own", got)
	}
}

func TestTheLiveLineIsErasedWhenTheRunsResultIsDrawnNotWhenTheCommandExits(t *testing.T) {
	inDeployFixture(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)

	executeRootOn(t, &bytes.Buffer{}, tty, "deployments", "ls")

	got := screen()
	_, afterResult, ok := strings.Cut(got, "✓ Done")
	if !ok {
		t.Fatalf("the terminal shows %q, want the run's result", got)
	}
	if strings.Contains(afterResult, "[check]") {
		t.Errorf("after the result the terminal shows %q, want the live line gone with the run", afterResult)
	}
}
