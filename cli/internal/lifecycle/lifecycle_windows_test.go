package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestACommandThatRunsPastItsTimeoutIsStoppedWithEveryProcessItStarted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")

	err := Run(context.Background(), Command{
		Line:    `start "" /b cmd /d /c "ping -n 4 127.0.0.1 >nul & echo survived>%MARKER%" & ping -n 30 127.0.0.1 >nul`,
		Dir:     t.TempDir(),
		Env:     map[string]string{"MARKER": marker},
		Timeout: 500 * time.Millisecond,
	})

	var timedOut *TimeoutError
	if !errors.As(err, &timedOut) {
		t.Fatalf("Run err = %v, want a TimeoutError", err)
	}
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a process cmd.exe started outlived the timeout (stat err %v), want every process the command started stopped", err)
	}
}
