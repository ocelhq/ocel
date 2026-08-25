package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/cli"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
)

func main() {
	if err := cli.Execute(); err != nil {
		if code, ok := exitsig.ExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
