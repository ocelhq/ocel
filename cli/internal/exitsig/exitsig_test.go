package exitsig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

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

func fakeForceKill(t *testing.T) (forceKill func(), callCount func() int) {
	t.Helper()
	var mu sync.Mutex
	var count int
	return func() {
			mu.Lock()
			defer mu.Unlock()
			count++
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return count
		}
}

func TestInterruptHandlerFirstSignalCancelsContext(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)
	forceKill, forceKillCalls := fakeForceKill(t)

	ctx, stop := InstallWithExit(context.Background(), &stderr, ch, time.Hour, forceKill, exit)
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
	if got := forceKillCalls(); got != 0 {
		t.Errorf("forceKill called %d times after only one signal, want a normal teardown to run instead", got)
	}
}

func TestInterruptHandlerSecondSignalForcesExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)
	forceKill, forceKillCalls := fakeForceKill(t)

	ctx, stop := InstallWithExit(context.Background(), &stderr, ch, time.Hour, forceKill, exit)
	defer stop()

	ch <- os.Interrupt
	<-ctx.Done()

	ch <- os.Interrupt

	if !waitFor(func() bool { return len(calls()) == 1 }, 2*time.Second) {
		t.Fatalf("exit was not called after a second signal; calls = %v", calls())
	}
	if got := calls(); got[0] != InterruptCode {
		t.Errorf("exit code = %d, want %d (the conventional interrupt status)", got[0], InterruptCode)
	}
	if !strings.Contains(stderr.String(), "Interrupted again") {
		t.Errorf("stderr = %q, want it to say a second interrupt happened", stderr.String())
	}
	if got := forceKillCalls(); got != 1 {
		t.Errorf("forceKill called %d times, want exactly 1 before the forced exit", got)
	}
}

func TestInterruptHandlerTerminateDoesNotCutTheWindowShort(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)
	forceKill, forceKillCalls := fakeForceKill(t)

	ctx, stop := InstallWithExit(context.Background(), &stderr, ch, time.Hour, forceKill, exit)
	defer stop()

	ch <- os.Interrupt
	<-ctx.Done()

	ch <- syscall.SIGTERM

	if waitFor(func() bool { return len(calls()) > 0 }, 200*time.Millisecond) {
		t.Fatalf("exit was called on a supervisor's SIGTERM; calls = %v", calls())
	}
	if got := forceKillCalls(); got != 0 {
		t.Errorf("forceKill called %d times, want the graceful shutdown left to finish", got)
	}
	if strings.Contains(stderr.String(), "Interrupted again") {
		t.Errorf("stderr = %q, want a SIGTERM not read as a second Ctrl-C", stderr.String())
	}

	ch <- os.Interrupt
	if !waitFor(func() bool { return len(calls()) == 1 }, 2*time.Second) {
		t.Fatalf("exit was not called after a real second interrupt; calls = %v", calls())
	}
}

func TestInterruptHandlerGracefulWindowExpiryForcesExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)
	forceKill, forceKillCalls := fakeForceKill(t)

	ctx, stop := InstallWithExit(context.Background(), &stderr, ch, 20*time.Millisecond, forceKill, exit)
	defer stop()

	ch <- os.Interrupt
	<-ctx.Done()

	if !waitFor(func() bool { return len(calls()) == 1 }, 2*time.Second) {
		t.Fatalf("exit was not called after the graceful window expired; calls = %v", calls())
	}
	if got := calls(); got[0] != InterruptCode {
		t.Errorf("exit code = %d, want %d", got[0], InterruptCode)
	}
	if strings.Contains(stderr.String(), "Interrupted again") {
		t.Errorf("stderr = %q, want the expiry path not to accuse a single Ctrl-C of being a second interrupt", stderr.String())
	}
	if !strings.Contains(stderr.String(), "did not finish") {
		t.Errorf("stderr = %q, want it to say the graceful window expired", stderr.String())
	}
	if got := forceKillCalls(); got != 1 {
		t.Errorf("forceKill called %d times, want exactly 1 when the graceful window itself expires", got)
	}
}

func TestInterruptHandlerStopPreventsForcedExit(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, calls := fakeExit(t)
	forceKill, forceKillCalls := fakeForceKill(t)

	ctx, stop := InstallWithExit(context.Background(), &stderr, ch, 20*time.Millisecond, forceKill, exit)

	ch <- os.Interrupt
	<-ctx.Done()
	stop()

	time.Sleep(100 * time.Millisecond)
	if got := calls(); len(got) != 0 {
		t.Errorf("exit called %v after stop(), want the handler to have shut down cleanly", got)
	}
	if got := forceKillCalls(); got != 0 {
		t.Errorf("forceKill called %d times after stop(), want the handler to have shut down cleanly", got)
	}
}

func TestInterruptHandlerStopIsSafeToCallTwice(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	ch := make(chan os.Signal, 2)
	exit, _ := fakeExit(t)
	forceKill, _ := fakeForceKill(t)

	_, stop := InstallWithExit(context.Background(), &stderr, ch, time.Hour, forceKill, exit)

	stop()
	stop()
}

func TestExitCodeMapsExitError(t *testing.T) {
	t.Parallel()

	code, ok := ExitCode(fmt.Errorf("build failed: %w", &ExitError{Code: 7}))
	if !ok || code != 7 {
		t.Errorf("ExitCode = (%d, %v), want (7, true)", code, ok)
	}
}

func TestExitCodeMapsCancellationToInterrupt(t *testing.T) {
	t.Parallel()

	code, ok := ExitCode(fmt.Errorf("read ocel.config.ts: %w", context.Canceled))
	if !ok || code != InterruptCode {
		t.Errorf("ExitCode = (%d, %v), want (%d, true)", code, ok, InterruptCode)
	}
}

func TestExitCodeLeavesOrdinaryErrorsAlone(t *testing.T) {
	t.Parallel()

	if code, ok := ExitCode(errors.New("boom")); ok {
		t.Errorf("ExitCode = (%d, true), want no mapping so the error is printed and reported as 1", code)
	}
	if code, ok := ExitCode(context.DeadlineExceeded); ok {
		t.Errorf("ExitCode = (%d, true), want a timeout not to look like a Ctrl-C", code)
	}
	if code, ok := ExitCode(nil); ok {
		t.Errorf("ExitCode = (%d, true), want no mapping for a nil error", code)
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
