package prompt

import (
	"io"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestInteractiveOnlyWhenBothEndsAreATerminal(t *testing.T) {
	t.Parallel()

	if New(io.Discard, strings.NewReader("y\n")).Interactive() {
		t.Error("Interactive() = true for a pipe on both ends, want false")
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})

	if !New(tty, tty).Interactive() {
		t.Error("Interactive() = false on a terminal, want true")
	}
	if New(tty, strings.NewReader("y\n")).Interactive() {
		t.Error("Interactive() = true when only the output is a terminal, want false")
	}
	if New(io.Discard, tty).Interactive() {
		t.Error("Interactive() = true when only the input is a terminal, want false")
	}
}
