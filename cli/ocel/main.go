package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/cli"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func main() {
	if err := cli.Execute(); err != nil {
		if code, ok := exitcode.Of(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
