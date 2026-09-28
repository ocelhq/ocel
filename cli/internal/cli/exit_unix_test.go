//go:build unix

package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

type terminalOutput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (o *terminalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *terminalOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func TestCtrlCDuringALinkLeavesItsTranscriptWithNoLiveLineAndExitsInterrupted(t *testing.T) {
	stalled := func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	twoOrganizations := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"1","name":"Acme","slug":"acme"},{"id":"2","name":"Initech","slug":"initech"}]`)
	}
	for _, tc := range []struct {
		name    string
		console http.HandlerFunc
		shown   string
	}{
		{name: "while the console is loading", console: stalled, shown: "[check] 0/1"},
		{name: "at a prompt", console: twoOrganizations, shown: "Select an organization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen, exited := linkOnATerminal(t, tc.console, tc.shown)

			var exitErr *exec.ExitError
			if !errors.As(exited, &exitErr) || exitErr.ExitCode() != 130 {
				t.Errorf("ocel link exited with %v, want status 130", exited)
			}
			_, afterResult, ok := strings.Cut(ansi.Strip(screen), "Link cancelled")
			if !ok {
				t.Fatalf("the terminal shows %q, want the run ended as cancelled", screen)
			}
			if strings.Contains(afterResult, "[check]") {
				t.Errorf("after the result the terminal shows %q, want no live line left drawn", afterResult)
			}
		})
	}
}

func linkOnATerminal(t *testing.T, console http.HandlerFunc, shown string) (screen string, exited error) {
	t.Helper()
	server := httptest.NewServer(console)
	t.Cleanup(server.Close)
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	cmd := exec.Command(self)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		rootArgsEnvVar+"=link",
		"OCEL_ACCESS_TOKEN=tok",
		"OCEL_CONSOLE_URL="+server.URL,
		"TERM=xterm-256color",
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })
	var out terminalOutput
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(drained)
	}()

	if !waitFor(func() bool { return strings.Contains(out.String(), shown) }, 10*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatalf("the terminal shows %q, want %q before Ctrl-C", out.String(), shown)
	}
	if _, err := ptmx.Write([]byte{3}); err != nil {
		t.Fatalf("type Ctrl-C: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case exited = <-done:
		select {
		case <-drained:
		case <-time.After(gracefulShutdownWindow / 2):
		}
	case <-time.After(gracefulShutdownWindow / 2):
		_ = cmd.Process.Kill()
		t.Fatalf("ocel link did not exit well within its %s shutdown window after Ctrl-C; the terminal shows %q", gracefulShutdownWindow, out.String())
	}
	return out.String(), exited
}
