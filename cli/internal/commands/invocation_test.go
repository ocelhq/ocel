package commands_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestACommandSharingItsTerminalWithAChildReportsAPlainTranscriptOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := commands.ShareTerminalWithChild(&cobra.Command{Use: "dev"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	invocation := commands.Invocation{
		Events: run.NewBus(time.Now),
		Presentation: func(io.Writer) terminal.Presentation {
			return terminal.Resolve(terminal.Conditions{ColorAsked: terminal.ColorNever, TTY: true, Width: 120, WidthMeasured: true})
		},
	}

	invocation.AttachCommandSink(cmd)
	_, begun, err := invocation.Events.Begin(context.Background(), "ocel dev", "")
	if err != nil {
		t.Fatal(err)
	}
	begun.Phase(progressv1.Phase_PHASE_UNSPECIFIED).Say("resolved API_TOKEN from .env")
	begun.End(&err)
	if err := invocation.Events.Close(); err != nil {
		t.Fatal(err)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want it left to the child", stdout.String())
	}
	if !strings.Contains(stderr.String(), "resolved API_TOKEN from .env") || strings.Contains(stderr.String(), "\x1b[") {
		t.Errorf("stderr = %q, want the run as plain lines with no live line redrawn over the child's output", stderr.String())
	}
}

func TestTheBrowserIsReachableOnlyFromAnInteractiveTerminalThatHasNotOptedOut(t *testing.T) {
	for _, tc := range []struct {
		name      string
		terminal  bool
		noBrowser string
		want      bool
	}{
		{name: "an interactive terminal", terminal: true, want: true},
		{name: "a redirected stdin", terminal: false, want: false},
		{name: "an opted-out terminal", terminal: true, noBrowser: "1", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(commands.NoBrowserEnvVar, tc.noBrowser)
			invocation := commands.Invocation{StdinIsTerminal: func(io.Reader) bool { return tc.terminal }}
			if got := invocation.IsBrowserReachable(strings.NewReader("")); got != tc.want {
				t.Errorf("IsBrowserReachable() = %v, want %v", got, tc.want)
			}
		})
	}
}
