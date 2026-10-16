module example.com/worker

go 1.27.0

require ocel.dev v0.0.0-00010101000000-000000000000

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	connectrpc.com/connect v1.20.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace ocel.dev => ../../../../sdk
