package childprocess

import (
	"io"
	"os"

	"golang.org/x/term"
)

type terminalState struct {
	fd    int
	state *term.State
}

func saveTerminal(stdin io.Reader) terminalState {
	f, ok := stdin.(*os.File)
	if !ok {
		return terminalState{fd: -1}
	}
	fd := int(f.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return terminalState{fd: -1}
	}
	return terminalState{fd: fd, state: state}
}

func (s terminalState) restore() {
	if s.fd < 0 || s.state == nil {
		return
	}
	_ = term.Restore(s.fd, s.state)
}
