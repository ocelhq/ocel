module github.com/ocelhq/ocel/scripts/pricingbridge

go 1.27.0

replace github.com/ocelhq/ocel/pkg => ../../pkg

require (
	github.com/ocelhq/ocel/pkg v0.0.0
	github.com/pulumi/inflector v0.1.1
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
