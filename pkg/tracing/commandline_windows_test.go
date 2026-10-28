package tracing_test

import (
	"syscall"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestACommandStartedFromAWholeCommandLineRecordsThatLine(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	line := `cmd.exe /d /s /c "exit 0"`
	cmd := tracing.Command(ctx, "cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}

	if err := cmd.Run(); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := tracingtest.Attributes(recorded.Only(t))[string(tracing.CommandLineKey)].AsString(); got != line {
		t.Errorf("process.command_line = %q, want %q: the hook line lives there, not in Args", got, line)
	}
}
