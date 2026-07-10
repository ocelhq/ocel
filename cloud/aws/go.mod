// The Ocel AWS provider binary. It lives in its own Go module so the heavy AWS
// SDK dependency tree it will pull in stays out of the CLI's and SDK's module
// graphs. Install: go install github.com/ocelhq/ocel/cloud/aws@latest -> binary `aws`.
//
// The `replace` pins the shared proto module to the local checkout (proto has no
// published tag yet); without it `go mod tidy` mis-resolves the path to the CLI
// module. Swap for a real `require github.com/ocelhq/ocel/pkg/proto vX.Y.Z` (and
// drop the replace) once proto is tagged. The root go.work wires this for dev.
module github.com/ocelhq/ocel/cloud/aws

go 1.25.3

require (
	connectrpc.com/connect v1.20.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0-00010101000000-000000000000
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/ocelhq/ocel/pkg/proto => ../../pkg/proto
