package session

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type inputAfterMarker struct {
	marker string
	input  io.Reader
}

func (a *inputAfterMarker) Read(p []byte) (int, error) {
	for range 2000 {
		if _, err := os.Stat(a.marker); err == nil {
			return a.input.Read(p)
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
	feed := &inputAfterMarker{marker: marker, input: strings.NewReader("streamed")}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stdout, stderr, code, err := run(ctx, feed, "sh", "-c", "touch "+marker+"; cat")
	if err != nil || code != 0 {
		t.Fatalf("run() = code %d, %v (%s)", code, err, stderr)
	}
	if stdout != "streamed" {
		t.Errorf("the command read %q, want %q", stdout, "streamed")
	}
}

func newSessionRunning(t *testing.T, script string) *Session {
	t.Helper()
	sshSaying(t, resolved, keyed, script)
	return &Session{target: Target{Host: "203.0.113.10", User: "ubuntu"}, dest: Destination{User: "ubuntu", Written: "203.0.113.10"}}
}

func TestRunLinesDeliversEachLineBeforeTheCommandExits(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-delivered")
	box := newSessionRunning(t, "echo first; while [ ! -e "+quotedForTest(marker)+" ]; do sleep 0.01; done; echo second")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var got []string
	err := box.RunLines(ctx, "logs", func(line Line) error {
		got = append(got, line.Text)
		return os.WriteFile(marker, nil, 0o644)
	})
	if err != nil {
		t.Fatalf("RunLines() = %v", err)
	}
	if want := []string{"first", "second"}; !slices.Equal(got, want) {
		t.Errorf("RunLines() delivered %q, want %q: the command held its second line back until the first had reached the caller", got, want)
	}
}

func TestRunLinesKeepsStdoutAndStderrApart(t *testing.T) {
	box := newSessionRunning(t, "echo out-1; echo err-1 >&2; echo out-2; echo err-2 >&2")

	got := map[Pipe][]string{}
	err := box.RunLines(context.Background(), "logs", func(line Line) error {
		got[line.Pipe] = append(got[line.Pipe], line.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("RunLines() = %v", err)
	}
	want := map[Pipe][]string{Stdout: {"out-1", "out-2"}, Stderr: {"err-1", "err-2"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("RunLines() delivered %v, want %v: each line carries the stream it was written to, in order within it", got, want)
	}
}

func TestRunLinesStopsTheCommandWhenTheContextIsCancelled(t *testing.T) {
	box := newSessionRunning(t, "echo started; exec sleep 30")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		returned <- box.RunLines(ctx, "logs --follow", func(Line) error {
			cancel()
			return nil
		})
	}()

	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("RunLines() = %v, want the cancellation: ssh killed on the caller's say-so is no failure of the host", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunLines() was still running a second after its context was cancelled")
	}
}

func TestRunLinesReturnsTheCallbacksError(t *testing.T) {
	box := newSessionRunning(t, "echo started; exec sleep 30")
	stopped := errors.New("the caller wants no more")

	returned := make(chan error, 1)
	go func() {
		returned <- box.RunLines(context.Background(), "logs --follow", func(Line) error { return stopped })
	}()

	select {
	case err := <-returned:
		if !errors.Is(err, stopped) {
			t.Errorf("RunLines() = %v, want the callback's own error %v", err, stopped)
		}
	case <-time.After(time.Second):
		t.Fatal("RunLines() kept the command running after its callback refused a line")
	}
}

func TestRunLinesCutsALineLongerThanAMebibyteAndKeepsReading(t *testing.T) {
	box := newSessionRunning(t, "head -c 1572864 /dev/zero | tr '\\0' a; echo; echo after")

	var got []string
	err := box.RunLines(context.Background(), "logs", func(line Line) error {
		got = append(got, line.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("RunLines() = %v, want the command to run to completion past its over-long line", err)
	}
	if len(got) != 2 || got[0] != strings.Repeat("a", 1<<20) || got[1] != "after" {
		lengths := make([]int, len(got))
		for i, text := range got {
			lengths[i] = len(text)
		}
		t.Errorf("RunLines() delivered lines of %v bytes, want the first mebibyte of the long line, then %q", lengths, "after")
	}
}

func TestRunLinesReportsTheLastOfALongStderr(t *testing.T) {
	box := newSessionRunning(t, "i=0; while [ $i -lt 2000 ]; do echo 'noise noise noise noise noise noise noise noise' >&2; i=$((i+1)); done; echo 'Permission denied (publickey).' >&2; exit 255")

	err := box.RunLines(context.Background(), "logs", func(Line) error { return nil })
	if got := refusedCode(t, err); got != refusal.CodeDenied {
		t.Errorf("RunLines() refused with %s, want %s: ssh's own last message comes after a long stderr", got, refusal.CodeDenied)
	}
}

func TestRunLinesAndStreamReportAnUnreachedHostAlike(t *testing.T) {
	box := newSessionRunning(t, "exit 255")

	_, streamed := box.Stream(context.Background(), "logs", nil)
	lined := box.RunLines(context.Background(), "logs", func(Line) error { return nil })
	if streamed == nil || lined == nil || streamed.Error() != lined.Error() {
		t.Errorf("RunLines() = %v, Stream() = %v: one unreached host reads the same either way", lined, streamed)
	}
}

func TestRunLinesReturnsNilForACommandThatSucceededBeforeTheContextWasCancelled(t *testing.T) {
	box := newSessionRunning(t, "(sleep 0.2; echo last) & exit 0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := box.RunLines(ctx, "logs", func(Line) error {
		cancel()
		return nil
	})
	if err != nil {
		t.Errorf("RunLines() = %v, want nil: the command had already exited 0 when the context was cancelled", err)
	}
}

func TestTwoSessionsToOneHostNeverShareAMaster(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no session on windows multiplexes")
	}
	first, second := multiplex(), multiplex()
	if first == "" || second == "" {
		t.Skip("this machine offers no cache directory to keep control sockets in")
	}
	if first == second {
		t.Errorf("two sessions multiplex over %q, so the first Close() takes down the master the second is still running commands over", first)
	}
}
