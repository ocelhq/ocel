module github.com/ocelhq/ocel/pkg/costkit

go 1.27.0

require (
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/shopspring/decimal v1.4.0
	google.golang.org/protobuf v1.36.12
)

require buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect

replace github.com/ocelhq/ocel/pkg/proto => ../proto
