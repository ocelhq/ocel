//go:build unix

package childprocess

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitCodeMapsASignalDeathToTheShellConvention(t *testing.T) {
	t.Parallel()

	var exitErr *exec.ExitError
	if err := exec.Command("sh", "-c", "kill -INT $$").Run(); !errors.As(err, &exitErr) {
		t.Fatalf("run = %v, want an *exec.ExitError", err)
	}
	if got := ExitCode(exitErr); got != 130 {
		t.Errorf("ExitCode = %d, want 130 rather than the -1 os/exec reports for a signalled process", got)
	}
}
