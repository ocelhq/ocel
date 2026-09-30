package terminal

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

type ptyScreen struct {
	mu   sync.Mutex
	seen bytes.Buffer
}

func (s *ptyScreen) shows(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.seen.String(), text)
}

func aPromptTerminal(t *testing.T) (*os.File, *os.File, *ptyScreen) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatalf("size the pty: %v", err)
	}
	screen := &ptyScreen{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			screen.mu.Lock()
			screen.seen.Write(buf[:n])
			screen.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return ptmx, tty, screen
}

func typeOnSight(t *testing.T, ptmx *os.File, screen *ptyScreen, sight, keys string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for !screen.shows(sight) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		for _, key := range keys {
			_, _ = ptmx.WriteString(string(key))
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func withinSeconds(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAnAttendedSelectMovesWithTheArrowKeysAndTakesEnter(t *testing.T) {
	ptmx, tty, screen := aPromptTerminal(t)
	typeOnSight(t, ptmx, screen, "Where should shop deploy?", "\x1b[B\r")

	chosen, answered, err := NewPrompt(tty, tty).Select(withinSeconds(t), "Where should shop deploy?", []Option{{Name: "aws"}, {Name: "gcp"}, {Name: "vps"}})
	if err != nil || !answered || chosen != "gcp" {
		t.Errorf("Select() = %q, %t, %v; want gcp, one down from the first", chosen, answered, err)
	}
}

func TestAnAttendedInputTakesWhatIsTyped(t *testing.T) {
	ptmx, tty, screen := aPromptTerminal(t)
	typeOnSight(t, ptmx, screen, "ssh", "203.0.113.7\r")

	typed, answered, err := NewPrompt(tty, tty).Input(withinSeconds(t), "ssh", "The machine to deploy onto")
	if err != nil || !answered || typed != "203.0.113.7" {
		t.Errorf("Input() = %q, %t, %v; want the address typed", typed, answered, err)
	}
}

func TestAnAttendedAwaitEnterGoesOnAtEnter(t *testing.T) {
	ptmx, tty, screen := aPromptTerminal(t)
	typeOnSight(t, ptmx, screen, "Press Enter", "\r")

	saved, err := NewPrompt(tty, tty).AwaitEnter(withinSeconds(t), "Press Enter once it's saved (or n to stop)")
	if err != nil || !saved {
		t.Errorf("AwaitEnter() = %t, %v; want Enter to go on", saved, err)
	}
}
