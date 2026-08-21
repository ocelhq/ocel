// Prototype only: the shape of the provider kit's root interface and its ports,
// stubbed so it compiles and can be read. Nothing here provisions anything.
module github.com/ocelhq/ocel/pkg/providerkit

go 1.26.6

require (
	connectrpc.com/connect v1.20.0
	connectrpc.com/validate v0.6.0
	github.com/ocelhq/ocel/pkg/channel v0.0.0-20260821173752-eac661f05a64
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.24.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	golang.org/x/exp v0.0.0-20250911091902-df9299821621 // indirect
	golang.org/x/text v0.29.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250922171735-9219d122eba9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250922171735-9219d122eba9 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/pkg/naming => ../naming

replace github.com/ocelhq/ocel/pkg/proto => ../proto

replace github.com/ocelhq/ocel/platform/edge/contract => ../../platform/edge/contract
