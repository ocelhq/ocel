package deployui

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// IsTerminal reports whether w is a terminal the renderer may animate and
// colour. It is the one place in the CLI that asks the OS this question —
// callers must check the writer they are about to write to, never
// os.Stdout, or colour codes leak into redirected output.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}
