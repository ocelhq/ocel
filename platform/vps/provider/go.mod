module github.com/ocelhq/ocel/platform/vps/provider

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/creack/pty v1.1.24
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/pkg/providerkit v0.0.0
	github.com/ocelhq/ocel/platform/edge/cloudflare/deploy v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.24.0 // indirect
	connectrpc.com/validate v0.6.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/aws/aws-sdk-go-v2 v1.43.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.14 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.0 // indirect
	github.com/aws/smithy-go v1.27.3 // indirect
	github.com/cloudflare/cloudflare-go/v4 v4.6.0 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/ocelhq/ocel/pkg/channel v0.0.0 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/exp v0.0.0-20250911091902-df9299821621 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250922171735-9219d122eba9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250922171735-9219d122eba9 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/pkg/providerkit => ../../../pkg/providerkit

replace github.com/ocelhq/ocel/pkg/channel => ../../../pkg/channel

replace github.com/ocelhq/ocel/pkg/naming => ../../../pkg/naming

replace github.com/ocelhq/ocel/pkg/proto => ../../../pkg/proto

replace github.com/ocelhq/ocel/platform/edge/contract => ../../edge/contract

replace github.com/ocelhq/ocel/platform/edge/cloudflare/deploy => ../../edge/cloudflare/deploy
