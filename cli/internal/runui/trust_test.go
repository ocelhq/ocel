package runui

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

type terminalAsker struct{}

func (terminalAsker) Attended() bool { return true }

func (terminalAsker) Confirm(context.Context, string) (bool, error) { return true, nil }

func TestTheTrustIsTheProcessTerminalNotTheProvidersLogStream(t *testing.T) {
	_, run, err := runtrace.Start(context.Background(), t.TempDir(), "ocel bootstrap production")
	if err != nil {
		t.Fatalf("runtrace.Start() = %v", err)
	}
	t.Cleanup(func() { run.Close() })

	var terminal bytes.Buffer
	host := provider.Trust{Ask: terminalAsker{}, Out: &terminal}
	ui := New(io.Discard, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { ui.Close() })

	trust := TrustFor(host, ui)
	if trust.Ask != host.Ask {
		t.Errorf("the trust asks through %#v, want the terminal the process was started on", trust.Ask)
	}
	if trust.Out != host.Out {
		t.Errorf("the trust offers on %#v, want the terminal, not the provider's log stream", trust.Out)
	}
	if trust.Out == ui.BuildWriter() {
		t.Error("the trust offers on the provider's log stream, where nobody would read it")
	}
	if trust.Suspend == nil {
		t.Error("the trust has no way to stand the live view down while it asks")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func waitForFrame(t *testing.T, terminal *syncBuffer) {
	t.Helper()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if terminal.String() != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the spinner drew nothing, so this run proves nothing about standing it down")
}

func TestTheSpinnerAStreamHandsOutStandsTheLiveViewDown(t *testing.T) {
	var terminal syncBuffer
	s := NewStream(&terminal, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight})
	t.Cleanup(func() { _ = s.Close() })

	spinner := s.Spin("Checking your setup")
	t.Cleanup(spinner.Stop)
	waitForFrame(t, &terminal)

	resume := TrustFor(provider.Trust{}, spinner).Suspend()
	terminal.Reset()
	time.Sleep(5 * frameRate)
	if drawn := terminal.String(); drawn != "" {
		t.Errorf("the spinner drew %q over the trust prompt", drawn)
	}

	resume()
	waitForFrame(t, &terminal)
}
