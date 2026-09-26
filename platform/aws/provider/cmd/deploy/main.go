package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, New: aws.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel aws provider:", err)
		os.Exit(1)
	}
}
