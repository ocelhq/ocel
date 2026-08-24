package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit"
	provider "github.com/ocelhq/ocel/platform/aws/provider"
)

var version = "dev"

func main() {
	if err := providerkit.Serve(providerkit.Spec{Version: version, New: provider.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel aws provider:", err)
		os.Exit(1)
	}
}
