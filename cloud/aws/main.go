// Command aws is the Ocel AWS provider binary (scaffold).
//
// It compiles to a standalone binary named `aws`
// (go install github.com/ocelhq/ocel/cloud/aws@latest). Real provisioning
// against AWS lands here later, pulling the AWS SDK into THIS module only.
package main

import (
	"fmt"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Reference the shared proto module so the scaffold exercises the cross-module
	// dependency wired through go.work; replace with the real provider entrypoint.
	fmt.Printf("ocel aws provider %s (scaffold) — knows %d resource types\n",
		version, len(resourcesv1.ResourceType_name))
}
