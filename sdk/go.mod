// The Ocel Go SDK: the library Go apps import to declare resources and talk to
// the dev server. It is its own module so consumers get a lean dependency graph
// — it depends on the shared proto module, never on the CLI module
// (github.com/ocelhq/ocel), so none of the CLI's deps (cobra, esbuild, keyring)
// reach SDK consumers.
//
// pkg/proto has no published module tag yet, so the `replace` below pins its
// import path to the local checkout: without it `go mod tidy` mis-resolves the
// path to the CLI module (which historically contained it) and drags in the
// CLI's whole dependency graph. Replace with a real `require
// github.com/ocelhq/ocel/pkg/proto vX.Y.Z` (and drop the replace) once proto is
// tagged. The root go.work also wires this for multi-module dev.
module github.com/ocelhq/ocel/sdk

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/ocelhq/ocel/pkg/proto => ../pkg/proto
