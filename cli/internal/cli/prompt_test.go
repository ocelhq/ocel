package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type blockingReader struct {
	unblock chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}

func TestConfirmYNCancelledContextReturnsPromptly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout bytes.Buffer
	reader := &blockingReader{unblock: make(chan struct{})}
	defer close(reader.unblock)

	done := make(chan struct{})
	var got bool
	var err error
	go func() {
		got, err = confirmYN(ctx, "Proceed?", &stdout, reader)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmYN did not return after context cancellation; it is still blocked in the stdin read")
	}

	if err == nil {
		t.Fatal("confirmYN() error = nil, want the context's cancellation error")
	}
	if got {
		t.Errorf("confirmYN() = true, want false on cancellation")
	}
}

func TestConfirmYNCancelledDuringReadDoesNotReportDecline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var stdout bytes.Buffer
	reader := &blockingReader{unblock: make(chan struct{})}
	defer close(reader.unblock)

	resultCh := make(chan struct {
		proceed bool
		err     error
	}, 1)
	go func() {
		proceed, err := confirmYN(ctx, "Proceed?", &stdout, reader)
		resultCh <- struct {
			proceed bool
			err     error
		}{proceed, err}
	}()

	cancel()

	select {
	case r := <-resultCh:
		if r.err == nil {
			t.Fatal("confirmYN() error = nil, want an error so the caller never treats this as a genuine decline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmYN did not return after context cancellation")
	}
}

func TestConfirmYNStillReadsARealAnswer(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	got, err := confirmYN(context.Background(), "Proceed?", &stdout, strings.NewReader("y\n"))
	if err != nil {
		t.Fatalf("confirmYN() error = %v", err)
	}
	if !got {
		t.Error("confirmYN() = false, want true for a y answer")
	}
	if !strings.Contains(stdout.String(), "Proceed? [y/N]") {
		t.Errorf("stdout = %q, want it to contain the prompt", stdout.String())
	}
}

func TestReadLineSecondCallWhileFirstStillAbandonedReturnsPromptly(t *testing.T) {
	// Not t.Parallel(): exercises the package-level stdin lock directly.

	stdinMu.Lock()
	defer stdinMu.Unlock()

	done := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := readLine(context.Background(), os.Stdin)
		done <- struct {
			line string
			err  error
		}{line, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("readLine() error = nil, want an error since a prior abandoned read still owns stdin")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLine hung instead of returning an error while stdin is still held by an abandoned read")
	}
}

func TestReadLineCancelledContextReturnsPromptly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	reader := &blockingReader{unblock: make(chan struct{})}
	defer close(reader.unblock)

	done := make(chan struct{})
	go func() {
		if _, err := readLine(ctx, reader); err == nil {
			t.Error("readLine() error = nil, want the context's cancellation error")
		}
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLine did not return after context cancellation")
	}
}
