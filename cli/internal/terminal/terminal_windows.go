//go:build windows

package terminal

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func enableVirtualTerminal(w io.Writer) error {
	f, ok := w.(*os.File)
	if !ok {
		return errors.New("the run's output is not a console")
	}
	console := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(console, &mode); err != nil {
		return err
	}
	return windows.SetConsoleMode(console, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
