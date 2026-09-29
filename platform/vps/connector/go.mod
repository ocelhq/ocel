module github.com/ocelhq/ocel/platform/vps/connector

go 1.27.0

replace github.com/ocelhq/ocel/platform/vps/provider => ../provider

require (
	github.com/ocelhq/ocel/pkg v0.0.0
	github.com/ocelhq/ocel/platform/vps/provider v0.0.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.25.2 // indirect
	connectrpc.com/connect v1.20.0 // indirect
	connectrpc.com/validate v0.6.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/google/go-containerregistry v0.21.7 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.4.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc/v3 v3.0.6 // indirect
	github.com/lestrrat-go/jwx/v3 v3.3.0 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	github.com/valyala/fastjson v1.6.10 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/exp v0.0.0-20260824195058-e88cd73687aa // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260825221802-da73d73af1c5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260825221802-da73d73af1c5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/pkg => ../../../pkg

replace github.com/ocelhq/ocel/platform/edge/cloudflare/deploy => ../../edge/cloudflare/deploy

replace github.com/ocelhq/ocel/platform/s3 => ../../s3
