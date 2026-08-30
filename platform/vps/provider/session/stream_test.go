package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type awaiting struct {
	marker string
	said   io.Reader
}

func (a *awaiting) Read(p []byte) (int, error) {
	for range 2000 {
		if _, err := os.Stat(a.marker); err == nil {
			return a.said.Read(p)
		}
		time.Sleep(time.Millisecond)
	}
	return 0, errors.New("the command never ran while its input was still arriving")
}

func TestACommandRunsWhileItsInputIsStillArriving(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the transfer feeds a posix shell")
	}
	marker := filepath.Join(t.TempDir(), "started")
	feed := &awaiting{marker: marker, said: strings.NewReader("carried")}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stdout, stderr, code, err := run(ctx, feed, "sh", "-c", "touch "+marker+"; cat")
	if err != nil || code != 0 {
		t.Fatalf("run() = code %d, %v (%s)", code, err, stderr)
	}
	if stdout != "carried" {
		t.Errorf("the command read %q, want %q", stdout, "carried")
	}
}
