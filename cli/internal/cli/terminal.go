package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/mattn/go-isatty"
)

// isTTY reports whether w is a real terminal (as opposed to a pipe, file,
// or in-memory buffer such as those used in tests).
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// isReaderTTY reports whether r is a real terminal.
func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// withSpinner runs fn while displaying label with an animated spinner, but
// only if stdout is a real terminal; otherwise it just prints label as a
// plain line with no animation, so piped/logged output doesn't fill with
// control characters.
func withSpinner(stdout io.Writer, label string, fn func() error) error {
	if !isTTY(stdout) {
		fmt.Fprintln(stdout, label)
		return fn()
	}

	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(stdout))
	s.Suffix = " " + label
	s.Start()
	defer s.Stop()

	return fn()
}
