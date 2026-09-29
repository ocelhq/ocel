package commands_test

import (
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/commands"
)

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
			if got := invocation.BrowserReachable(strings.NewReader("")); got != tc.want {
				t.Errorf("BrowserReachable() = %v, want %v", got, tc.want)
			}
		})
	}
}
