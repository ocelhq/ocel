package lifecycle

import (
	"testing"
)

func TestACommandLineReachesCmdExeVerbatimWithItsQuotesUnescaped(t *testing.T) {
	line := `drizzle-kit migrate --config "config files\drizzle.ts" && echo "done"`

	cmd := newShellCommand(line)

	want := `cmd.exe /d /s /c "` + line + `"`
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("the command line handed to Windows is %+v, want %q", cmd.SysProcAttr, want)
	}
}
