package main

import (
	"os"

	"github.com/ocelhq/ocel/platform/realtime/gatewayserver"
)

func main() {
	os.Exit(gatewayserver.Run(os.Args[1:], gatewayserver.OpenMemoryBus))
}
