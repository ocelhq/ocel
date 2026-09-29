package main

import (
	"os"

	"github.com/ocelhq/ocel/cli/internal/commands/root"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func main() {
	if err := root.Execute(); err != nil {
		if code, ok := exitcode.Of(err); ok {
			os.Exit(code)
		}
		terminal.PrintFailure(os.Stderr, err)
		os.Exit(1)
	}
}
