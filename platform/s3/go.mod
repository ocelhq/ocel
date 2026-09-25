module github.com/ocelhq/ocel/platform/s3

go 1.27.0

replace github.com/ocelhq/ocel/pkg/providerkit => ../../pkg/providerkit

replace github.com/ocelhq/ocel/pkg/providerkit/pulumi => ../../pkg/providerkit/pulumi

replace github.com/ocelhq/ocel/pkg/connectorkit => ../../pkg/connectorkit

replace github.com/ocelhq/ocel/pkg/channel => ../../pkg/channel

replace github.com/ocelhq/ocel/pkg/naming => ../../pkg/naming

replace github.com/ocelhq/ocel/pkg/proto => ../../pkg/proto

replace github.com/ocelhq/ocel/pkg/configdoc => ../../pkg/configdoc

replace github.com/ocelhq/ocel/pkg/constants => ../../pkg/constants

replace github.com/ocelhq/ocel/pkg/costkit => ../../pkg/costkit

replace github.com/ocelhq/ocel/pkg/runtimekit => ../../pkg/runtimekit

replace github.com/ocelhq/ocel/pkg/target => ../../pkg/target

replace github.com/ocelhq/ocel/platform/edge/contract => ../edge/contract

require (
	connectrpc.com/connect v1.20.0
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/credentials v1.19.30
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.0
	github.com/aws/smithy-go v1.28.1
	github.com/ocelhq/ocel/pkg/constants v0.0.0
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/pkg/providerkit v0.0.0-00010101000000-000000000000
	github.com/ocelhq/ocel/pkg/runtimekit v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.25.2 // indirect
	connectrpc.com/validate v0.6.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.14 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.32 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/ocelhq/ocel/pkg/channel v0.0.0 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	golang.org/x/exp v0.0.0-20260824195058-e88cd73687aa // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260825221802-da73d73af1c5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260825221802-da73d73af1c5 // indirect
)
