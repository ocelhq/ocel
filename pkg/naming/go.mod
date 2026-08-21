module github.com/ocelhq/ocel/pkg/naming

go 1.26.6

require github.com/ocelhq/ocel/pkg/proto v0.0.0

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	google.golang.org/protobuf v1.36.12
)

replace github.com/ocelhq/ocel/pkg/proto => ../proto
