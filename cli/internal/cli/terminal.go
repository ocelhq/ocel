package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/ocelhq/ocel/cli/internal/deployui"
)

func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func withSpinner(stdout io.Writer, label string, fn func() error) error {
	if !deployui.IsTerminal(stdout) {
		fmt.Fprintln(stdout, label)
		return fn()
	}

	s := deployui.StartSpinner(stdout, label)
	defer s.Stop()

	return fn()
}
