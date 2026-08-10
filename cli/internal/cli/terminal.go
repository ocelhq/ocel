package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/mattn/go-isatty"
)

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func isReaderTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

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
