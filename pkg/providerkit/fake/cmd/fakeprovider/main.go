package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(providerserver.Config{Version: version, New: fake.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel fake provider:", err)
		os.Exit(1)
	}
}
