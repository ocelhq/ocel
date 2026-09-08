package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var version = "dev"

func main() {
	if err := providerkit.Serve(providerkit.Spec{Version: version, New: gcp.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel gcp provider:", err)
		os.Exit(1)
	}
}
