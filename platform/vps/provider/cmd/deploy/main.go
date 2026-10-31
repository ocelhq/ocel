package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/spanexport"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/host/assets"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, ExportSpans: spanexport.Start, New: vps.New(host.NewAssets(assets.FS))}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel vps provider:", err)
		os.Exit(1)
	}
}
