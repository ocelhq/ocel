package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/ocelhq/ocel/cli/internal/deployui"
)

// isReaderTTY is the one terminal-detection helper deployui does not cover:
// it answers whether stdin is interactive, for confirm prompts, not
// whether a target writer may be animated or coloured.
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
