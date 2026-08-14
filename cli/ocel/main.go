package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		if code, ok := cli.ExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
