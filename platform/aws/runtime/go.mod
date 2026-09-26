module github.com/ocelhq/ocel/platform/aws/runtime

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/aws/aws-lambda-go v1.54.0
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/credentials v1.19.30
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.20.51
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.60.0
	github.com/aws/aws-sdk-go-v2/service/kms v1.55.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.0
	github.com/aws/smithy-go v1.28.1
	github.com/ocelhq/ocel/pkg v0.0.0
	github.com/ocelhq/ocel/platform/aws/provider v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
	github.com/ocelhq/ocel/platform/s3 v0.0.0
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.25.2 // indirect
	connectrpc.com/validate v0.6.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.14 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.31 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.38 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.35.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/google/go-containerregistry v0.21.7 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	golang.org/x/exp v0.0.0-20260824195058-e88cd73687aa // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260825221802-da73d73af1c5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260825221802-da73d73af1c5 // indirect
)

replace github.com/ocelhq/ocel/platform/edge/contract => ../../edge/contract

replace github.com/ocelhq/ocel/platform/edge/cloudflare/deploy => ../../edge/cloudflare/deploy

replace github.com/ocelhq/ocel/platform/aws/provider => ../provider

replace github.com/ocelhq/ocel/platform/s3 => ../../s3

replace github.com/ocelhq/ocel/pkg => ../../../pkg

replace github.com/ocelhq/ocel/pkg/providerkit/pulumi => ../../../pkg/providerkit/pulumi
