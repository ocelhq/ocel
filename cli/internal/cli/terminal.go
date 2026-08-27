package cli

import (
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/runui"
)

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
