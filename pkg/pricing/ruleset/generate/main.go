package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: generate <manifest.json>")
		os.Exit(2)
	}
	encoded, err := ruleset.ManifestJSON()
	if err == nil {
		err = os.WriteFile(os.Args[1], encoded, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
