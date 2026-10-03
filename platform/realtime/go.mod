module github.com/ocelhq/ocel/platform/realtime

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/coder/websocket v1.8.15
	github.com/ocelhq/ocel/pkg v0.0.0
	google.golang.org/protobuf v1.36.12
)

require buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect

replace github.com/ocelhq/ocel/pkg => ../../pkg
