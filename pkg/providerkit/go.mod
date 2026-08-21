// Prototype only: the shape of the provider kit's root interface and its ports,
// stubbed so it compiles and can be read. Nothing here provisions anything.
module github.com/ocelhq/ocel/pkg/providerkit

go 1.26.6

require (
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	github.com/ocelhq/ocel/pkg/proto v0.0.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/pkg/naming => ../naming

replace github.com/ocelhq/ocel/pkg/proto => ../proto

replace github.com/ocelhq/ocel/platform/edge/contract => ../../platform/edge/contract
