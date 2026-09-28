module github.com/ocelhq/ocel/scripts/redactvet

go 1.27.0

require (
	github.com/ocelhq/ocel/pkg v0.0.0
	golang.org/x/tools v0.50.0
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)

replace github.com/ocelhq/ocel/pkg => ../../pkg
