// Command ocel is the Ocel CLI.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
