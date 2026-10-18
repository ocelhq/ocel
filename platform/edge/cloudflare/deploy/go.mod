module github.com/ocelhq/ocel/platform/edge/cloudflare/deploy

go 1.27.0

require (
	github.com/cloudflare/cloudflare-go/v4 v4.6.0
	github.com/ocelhq/ocel/pkg v0.0.0
	github.com/shopspring/decimal v1.4.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	connectrpc.com/connect v1.20.0 // indirect
	github.com/google/go-containerregistry v0.21.7 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/pkg => ../../../../pkg
