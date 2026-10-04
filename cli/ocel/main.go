package main

import (
	"os"

	"github.com/ocelhq/ocel/cli/internal/commands/root"
)

func main() {
	os.Exit(root.Execute())
}
