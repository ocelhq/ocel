package prompt

import (
	"io"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestAttendedIsBothEndsAndInteractiveIsWhoeverCanAnswer(t *testing.T) {
	t.Parallel()

	if New(io.Discard, strings.NewReader("y\n")).Attended() {
		t.Error("Attended() = true for a pipe on both ends, want false")
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})

	if !New(tty, tty).Attended() {
		t.Error("Attended() = false with a terminal on both ends, want true")
	}
	if New(tty, strings.NewReader("y\n")).Attended() {
		t.Error("Attended() = true when only the output is a terminal, want false — nobody is there to answer")
	}
	if !Interactive(tty) {
		t.Error("Interactive() = false with a terminal on stdin, want true — that is where an answer comes from")
	}
	if New(io.Discard, tty).Attended() {
		t.Error("Attended() = true with a piped output, want false — the question would go where nobody can read it")
	}
}
