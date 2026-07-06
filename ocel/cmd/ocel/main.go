// Command ocel is the Ocel CLI.
package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
