package cmddeps_test

import (
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
)

func TestBrowserReachable(t *testing.T) {
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
			t.Setenv(cmddeps.NoBrowserEnvVar, tc.noBrowser)
			deps := cmddeps.Deps{StdinIsTerminal: func(io.Reader) bool { return tc.terminal }}
			if got := deps.BrowserReachable(strings.NewReader("")); got != tc.want {
				t.Errorf("BrowserReachable() = %v, want %v", got, tc.want)
			}
		})
	}
}
