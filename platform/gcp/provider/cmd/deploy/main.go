package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, New: gcp.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel gcp provider:", err)
		os.Exit(1)
	}
}
