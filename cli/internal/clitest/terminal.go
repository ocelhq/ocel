package clitest

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func OpenTerminal(t *testing.T) (tty *os.File, screen func() string) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})
	var (
		mu  sync.Mutex
		got bytes.Buffer
	)
	drained := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			mu.Lock()
			got.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				close(drained)
				return
			}
		}
	}()
	return tty, func() string {
		tty.Close()
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		defer mu.Unlock()
		return got.String()
	}
}

func UnderJSONOnATerminal(t *testing.T, invocation *commands.Invocation) (tty *os.File, screen func() string) {
	t.Helper()
	invocation.StdinIsTerminal = func(r io.Reader) bool { return terminal.IsTerminal(r) }
	invocation.IsJSON = func() bool { return true }
	return OpenTerminal(t)
}

func FinishWithin(t *testing.T, limit time.Duration, fn func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- fn(ctx) }()
	select {
	case err := <-finished:
		return err
	case <-time.After(limit + 5*time.Second):
		t.Fatalf("still running after %s: something is waiting for an answer", limit)
		return nil
	}
}
