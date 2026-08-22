package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

var version = "dev"

func main() {
	if err := providerkit.Serve(providerkit.Spec{Version: version, New: vps.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel vps provider:", err)
		os.Exit(1)
	}
}
