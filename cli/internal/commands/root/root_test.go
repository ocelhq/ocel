package root

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func TestTheRootFlagsFeedTheOneResolver(t *testing.T) {
	set := &flags{logFormat: string(terminal.FormatJSON), verbose: true}

	p := set.presentation(&bytes.Buffer{})
	if p.Format != terminal.FormatJSON {
		t.Errorf("Format = %q, want the --log-format flag to reach the resolver", p.Format)
	}
	if !p.Verbose {
		t.Errorf("Verbose = false, want the --verbose flag to reach the resolver")
	}
}

func executeRoot(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	executeRootOn(t, &out, &errOut, args...)
	return out.String(), errOut.String()
}

func executeRootOn(t *testing.T, stdout, stderr io.Writer, args ...string) {
	t.Helper()
	ocel := newCommand()
	ocel.root.SetArgs(args)
	ocel.root.SetOut(stdout)
	ocel.root.SetErr(stderr)
	if err := ocel.execute(); err != nil {
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

func inADeployedProject(t *testing.T) {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.RecordPromotions(t, project, "promo-1", "promo-2")
	t.Chdir(project.Root)
}

func TestLogFormatJSONAttachesOnlyTheJSONSink(t *testing.T) {
	inADeployedProject(t)

	stdout, stderr := executeRoot(t, "--log-format", "json", "deployments", "prune", "--yes")

	var ended bool
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not JSON, want every line from the one JSON sink: %v", line, err)
		}
		_, ended = ev["summary"]
	}
	if !ended {
		t.Errorf("stdout = %q, want the run's events ending in its summary", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing beside the one JSON stream", stderr)
	}
}

func TestTheHumanLogFormatAttachesOnlyTheGroupedSink(t *testing.T) {
	inADeployedProject(t)

	stdout, _ := executeRoot(t, "deployments", "prune", "--yes")

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
	inADeployedProject(t)
	tty, screen := aTerminal(t, "xterm-256color", 40)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune", "--yes")

	if got := screen(); !strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the transcript drawn in live-line frames", got)
	}
}

func TestADumbTerminalGetsTheGroupedView(t *testing.T) {
	inADeployedProject(t)
	tty, screen := aTerminal(t, "dumb", 80)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune", "--yes")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestATerminalNarrowerThanFortyColumnsGetsTheGroupedView(t *testing.T) {
	inADeployedProject(t)
	tty, screen := aTerminal(t, "xterm-256color", 39)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune", "--yes")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestATerminalThatReportsNoWidthGetsTheGroupedViewWhateverColumnsSays(t *testing.T) {
	inADeployedProject(t)
	t.Setenv("COLUMNS", "120")
	tty, screen := aTerminal(t, "xterm-256color", 0)

	executeRootOn(t, tty, &bytes.Buffer{}, "deployments", "prune", "--yes")

	if got := screen(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("the terminal shows %q, want the grouped transcript with no live line", got)
	}
}

func TestAPipedStdoutGetsTheGroupedViewEvenWithATerminalOnStderr(t *testing.T) {
	inADeployedProject(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)
	var stdout bytes.Buffer

	executeRootOn(t, &stdout, tty, "deployments", "prune", "--yes")

	if got := stdout.String(); strings.Contains(got, liveFrame) || !strings.Contains(got, "Pruned") {
		t.Errorf("stdout = %q, want the grouped transcript with no live line", got)
	}
	if got := screen(); got != "" {
		t.Errorf("the terminal on stderr shows %q, want nothing: the run draws on stdout", got)
	}
}

func TestACommandWhoseStdoutIsItsDataDrawsTheLiveLineOnAStderrTerminal(t *testing.T) {
	inADeployedProject(t)
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
	inADeployedProject(t)
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
	inADeployedProject(t)

	stdout, stderr := executeRoot(t, "--log-format", "json", "deployments", "ls")

	if !strings.Contains(stdout, "promo-2") || strings.Contains(stdout, "{") {
		t.Errorf("stdout = %q, want the promotions table alone", stdout)
	}
	if evs := clitest.RunEvents(t, stderr); len(evs) == 0 || !evs[len(evs)-1].GetSummary().GetSuccess() {
		t.Errorf("stderr = %q, want the run's events ending in its summary", stderr)
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
		cmd, _, err := newCommand().root.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		if got := commands.ChooseRunOutput(cmd); got != &stderr {
			t.Errorf("ocel %s draws its run on stdout, want stderr so stdout holds only its data", strings.Join(path, " "))
		}
	}
}

func TestEveryCommandThatReportsThroughItsRunIsMarkedToDrawItOnStdout(t *testing.T) {
	for _, path := range [][]string{{"deploy"}, {"domain", "use"}, {"domain", "add"}, {"connector", "add"}, {"connector", "rm"}, {"deployments", "prune"}} {
		cmd, _, err := newCommand().root.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", path, err)
		}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		if got := commands.ChooseRunOutput(cmd); got != &stdout {
			t.Errorf("ocel %s draws its run on stderr, want stdout", strings.Join(path, " "))
		}
	}
}

func TestACommandWhoseDataAndRunShareOneTerminalDrawsTheGroupedViewSoNoLineOfDataLandsOnTheLiveRow(t *testing.T) {
	inADeployedProject(t)
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

func TestACommandThatPrintsItsReportOnlyOnceItsRunEndsDrawsTheLiveLineOnTheTerminalItsReportShares(t *testing.T) {
	inADeployedProject(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)

	executeRootOn(t, tty, tty, "doctor")

	got := screen()
	if !strings.Contains(got, liveFrame) {
		t.Errorf("the terminal shows %q, want the doctor's run drawn in live-line frames", got)
	}
	_, report, ok := strings.Cut(got, "✓ Doctor finished")
	if !ok || !strings.Contains(ansi.Strip(report), "\nProject  ") {
		t.Errorf("the terminal shows %q, want the report on rows of its own after the run's result", got)
	}
}

func TestTheLiveLineIsErasedWhenTheRunsResultIsDrawnNotWhenTheCommandExits(t *testing.T) {
	inADeployedProject(t)
	tty, screen := aTerminal(t, "xterm-256color", 80)

	executeRootOn(t, &bytes.Buffer{}, tty, "deployments", "ls")

	got := screen()
	_, afterResult, ok := strings.Cut(got, "✓ Deployments ls finished")
	if !ok {
		t.Fatalf("the terminal shows %q, want the run's result", got)
	}
	if strings.Contains(afterResult, "[check]") {
		t.Errorf("after the result the terminal shows %q, want the live line gone with the run", afterResult)
	}
}

func TestGenerateBindingsAndLinkAreEachTheirOwnCommandOffTheRoot(t *testing.T) {
	rootCmd := newCommand().root
	for _, name := range []string{"generate", "bindings", "link", "unlink"} {
		if cmd, _, err := rootCmd.Find([]string{name}); err != nil || cmd.Name() != name || cmd.Parent() != rootCmd {
			t.Errorf("`ocel %s` does not hang off the root command", name)
		}
	}
	generate, _, _ := rootCmd.Find([]string{"generate"})
	if !strings.Contains(generate.Long, "no login, no provider") {
		t.Errorf("`ocel generate` long = %q, want the promise it keeps", generate.Long)
	}
	bindingsGenerate, _, _ := rootCmd.Find([]string{"bindings", "generate"})
	if bindingsGenerate == generate || bindingsGenerate.Parent().Name() != "bindings" {
		t.Errorf("`ocel bindings generate` resolves to %v, want the bindings command's own generate", bindingsGenerate.CommandPath())
	}
}

func TestTheConfigFlagWinsOverTheConfigEnvironmentVariable(t *testing.T) {
	flag := newCommand().root.PersistentFlags().Lookup("config")
	if flag == nil {
		t.Fatal("`ocel` does not accept --config")
	}
	if flag.Shorthand != "c" {
		t.Errorf("--config shorthand = %q, want %q", flag.Shorthand, "c")
	}

	t.Run("neither the flag nor the env is set", func(t *testing.T) {
		set := &flags{config: ""}
		if got := set.explicitConfigPath(); got != "" {
			t.Errorf("explicitConfigPath() = %q, want the empty path that leaves discovery alone", got)
		}
	})

	t.Run("the env alone is honoured", func(t *testing.T) {
		t.Setenv("OCEL_CONFIG", "from-env.ts")
		set := &flags{config: ""}
		if got := set.explicitConfigPath(); got != "from-env.ts" {
			t.Errorf("explicitConfigPath() = %q, want %q", got, "from-env.ts")
		}
	})

	t.Run("the flag alone is honoured", func(t *testing.T) {
		set := &flags{config: "from-flag.ts"}
		if got := set.explicitConfigPath(); got != "from-flag.ts" {
			t.Errorf("explicitConfigPath() = %q, want %q", got, "from-flag.ts")
		}
	})

	t.Run("the flag wins over the env", func(t *testing.T) {
		t.Setenv("OCEL_CONFIG", "from-env.ts")
		set := &flags{config: "from-flag.ts"}
		if got := set.explicitConfigPath(); got != "from-flag.ts" {
			t.Errorf("explicitConfigPath() = %q, want --config to win over OCEL_CONFIG", got)
		}
	})
}

func TestConfigFlagPathThatNamesNothingRefuses(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

	set := &flags{config: filepath.Join(".", "nope.ts")}
	_, err := newInvocation(run.NewBus(time.Now), set).LoadProject(context.Background(), root)
	if err == nil {
		t.Fatal("LoadProject err = nil, want a refusal for a --config path that names nothing")
	}
	if !strings.Contains(err.Error(), filepath.Join(root, "nope.ts")) {
		t.Fatalf("LoadProject err = %v, want it to name the path --config asked for", err)
	}
}
