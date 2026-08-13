package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeExit lets a test observe a would-be os.Exit call without actually
// terminating the test binary.
func fakeExit(t *testing.T) (exit func(int), calls func() []int) {
	t.Helper()
	var mu sync.Mutex
	var codes []int
	return func(code int) {
			mu.Lock()
			defer mu.Unlock()
			codes = append(codes, code)
		}, func() []int {
			mu.Lock()
			defer mu.Unlock()
			return append([]int(nil), codes...)
		}
}

func TestInterruptHandlerFirstSignalCancelsContext(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)

	ctx, stop := interruptHandlerWithExit(context.Background(), &stderr, ch, time.Hour, exit)
	defer stop()

	select {
	case <-ctx.Done():
		t.Fatal("context cancelled before any signal was sent")
	default:
	}

	ch <- os.Interrupt

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled after the first signal")
	}

	if got := calls(); len(got) != 0 {
		t.Errorf("exit called %v after only one signal, want no forced exit", got)
	}
}

func TestInterruptHandlerSecondSignalForcesExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)

	ctx, stop := interruptHandlerWithExit(context.Background(), &stderr, ch, time.Hour, exit)
	defer stop()

	ch <- os.Interrupt
	<-ctx.Done()

	ch <- os.Interrupt

	if !waitFor(func() bool { return len(calls()) == 1 }, 2*time.Second) {
		t.Fatalf("exit was not called after a second signal; calls = %v", calls())
	}
	if got := calls(); got[0] != interruptExitCode {
		t.Errorf("exit code = %d, want %d (the conventional interrupt status)", got[0], interruptExitCode)
	}
	if !strings.Contains(stderr.String(), "resources may be mid-flight") {
		t.Errorf("stderr = %q, want a warning that the exit is unclean", stderr.String())
	}
}

func TestInterruptHandlerGracefulWindowExpiryForcesExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)

	ctx, stop := interruptHandlerWithExit(context.Background(), &stderr, ch, 20*time.Millisecond, exit)
	defer stop()

	ch <- os.Interrupt
	<-ctx.Done()

	if !waitFor(func() bool { return len(calls()) == 1 }, 2*time.Second) {
		t.Fatalf("exit was not called after the graceful window expired; calls = %v", calls())
	}
	if got := calls(); got[0] != interruptExitCode {
		t.Errorf("exit code = %d, want %d", got[0], interruptExitCode)
	}
}

func TestInterruptHandlerStopPreventsForcedExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)

	ctx, stop := interruptHandlerWithExit(context.Background(), &stderr, ch, 20*time.Millisecond, exit)

	ch <- os.Interrupt
	<-ctx.Done()
	stop()

	time.Sleep(100 * time.Millisecond)
	if got := calls(); len(got) != 0 {
		t.Errorf("exit called %v after stop(), want the handler to have shut down cleanly", got)
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
