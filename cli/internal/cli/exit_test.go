package cli

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/exitsig"
)

func TestAppExitErrorReportsInterruptWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, err := range []error{nil, context.Canceled, &exitsig.ExitError{Code: 255}} {
		got := appExitError(ctx, err)
		var exitErr *exitsig.ExitError
		if !errors.As(got, &exitErr) || exitErr.Code != exitsig.InterruptCode {
			t.Errorf("appExitError(cancelled, %v) = %v, want *exitsig.ExitError with code %d", err, got, exitsig.InterruptCode)
		}
	}
}

func TestAppExitErrorKeepsTheAppsCodeWhenNotCancelled(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 3")
	waitErr := cmd.Run()

	got := appExitError(context.Background(), waitErr)
	var exitErr *exitsig.ExitError
	if !errors.As(got, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("appExitError = %v, want *exitsig.ExitError with code 3", got)
	}
	if got := appExitError(context.Background(), nil); got != nil {
		t.Errorf("appExitError(nil) = %v, want nil", got)
	}
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
