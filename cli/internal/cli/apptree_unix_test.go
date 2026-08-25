//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/exitsig"
)

func TestWaitExitErrorMapsASignalDeathToTheShellConvention(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "kill -INT $$")
	err := waitExitError(cmd.Run())

	var exitErr *exitsig.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("waitExitError = %v, want an *exitsig.ExitError", err)
	}
	if exitErr.Code != exitsig.InterruptCode {
		t.Errorf("ExitError.Code = %d, want %d rather than the -1 os/exec reports for a signalled process", exitErr.Code, exitsig.InterruptCode)
	}
}

func TestSignalAppTreeReportsAnAlreadyGoneProcess(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run fixture: %v", err)
	}

	if err := terminateAppTree(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("terminateAppTree on a finished process = %v, want os.ErrProcessDone so os/exec does not invent a context.Canceled", err)
	}
}
