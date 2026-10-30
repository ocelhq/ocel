package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/spanexport"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, ExportSpans: spanexport.Start, New: gcp.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel gcp provider:", err)
		os.Exit(1)
	}
}
