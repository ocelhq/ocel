package root

import (
	"errors"
	"runtime"
	"strings"
)

const (
	panicExitCode = 2
	modulePrefix  = "github.com/ocelhq/ocel/"
)

var errPanicked = errors.New("panicked")

func findPanicFunction() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(0, pcs)])
	unwinding := false
	for {
		frame, more := frames.Next()
		if frame.Function == "runtime.gopanic" {
			unwinding = true
		} else if function, ours := strings.CutPrefix(frame.Function, modulePrefix); unwinding && ours {
			return function
		}
		if !more {
			return ""
		}
	}
}
