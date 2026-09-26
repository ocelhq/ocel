package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, New: vps.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel vps provider:", err)
		os.Exit(1)
	}
}
