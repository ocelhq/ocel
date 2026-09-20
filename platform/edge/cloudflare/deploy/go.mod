module github.com/ocelhq/ocel/platform/edge/cloudflare/deploy

go 1.27.0

require (
	github.com/aws/aws-sdk-go-v2 v1.43.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.0
	github.com/aws/smithy-go v1.27.3
	github.com/cloudflare/cloudflare-go/v4 v4.6.0
	github.com/ocelhq/ocel/pkg/costkit v0.0.0
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
	github.com/shopspring/decimal v1.4.0
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
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ocelhq/ocel/platform/edge/contract => ../../contract

replace github.com/ocelhq/ocel/pkg/costkit => ../../../../pkg/costkit

replace github.com/ocelhq/ocel/pkg/proto => ../../../../pkg/proto

replace github.com/ocelhq/ocel/pkg/naming => ../../../../pkg/naming
