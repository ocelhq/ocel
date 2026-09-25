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
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/pkg/providerkit v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.14 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.32 // indirect
)
