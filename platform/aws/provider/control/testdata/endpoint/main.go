package main

import (
	"context"
	"os"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/spanexport"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

func main() {
	err := providerserver.Serve(providerserver.Config{Version: "test", ExportSpans: spanexport.Start, New: func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
		return control.New(ctx, settings)
	}})
	if err != nil {
		os.Exit(1)
	}
}
