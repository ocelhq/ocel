package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/ocelhq/ocel/cli/internal/runui"
)

func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func withSpinner(stdout io.Writer, label string, fn func() error) error {
	present := presentation(stdout)
	if !present.TTY {
		fmt.Fprintln(stdout, label)
		return fn()
	}

	s := runui.StartSpinner(present, stdout, label)
	defer s.Stop()

	return fn()
}
