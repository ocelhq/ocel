package main

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

var version = "dev"

func main() {
	if err := providerkit.Serve(providerkit.Spec{Version: version, New: fake.New}); err != nil {
		fmt.Fprintln(os.Stderr, "ocel fake provider:", err)
		os.Exit(1)
	}
}
