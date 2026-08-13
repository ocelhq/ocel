module github.com/ocelhq/ocel/cli

go 1.25.11

require (
	connectrpc.com/connect v1.20.0
	github.com/briandowns/spinner v1.23.2
	github.com/evanw/esbuild v0.28.1
	github.com/fsnotify/fsnotify v1.10.1
	github.com/mattn/go-isatty v0.0.22
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/spf13/cobra v1.10.2
	github.com/zalando/go-keyring v0.2.8
	go.opentelemetry.io/otel v1.45.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.45.0
	go.opentelemetry.io/otel/sdk v1.45.0
	go.opentelemetry.io/otel/trace v1.45.0
	go.opentelemetry.io/proto/otlp v1.11.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/grpc v1.83.0 // indirect
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/air-verse/air v1.65.3 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bep/godartsass/v2 v2.5.0 // indirect
	github.com/bep/golibsass v1.2.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/fatih/color v1.18.0
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/gohugoio/hugo v0.149.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/ocelhq/ocel/pkg/channel v0.0.0-00010101000000-000000000000
	github.com/ocelhq/ocel/pkg/naming v0.0.0-00010101000000-000000000000
	github.com/ocelhq/ocel/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0-00010101000000-000000000000
	github.com/pelletier/go-toml v1.9.5 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/spf13/afero v1.14.0 // indirect
	github.com/spf13/cast v1.9.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tdewolff/parse/v2 v2.8.3 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	github.com/air-verse/air
	google.golang.org/protobuf/cmd/protoc-gen-go
)

replace github.com/ocelhq/ocel/pkg/channel => ../pkg/channel

replace github.com/ocelhq/ocel/pkg/proto => ../pkg/proto

replace github.com/ocelhq/ocel/pkg/naming => ../pkg/naming

replace github.com/ocelhq/ocel/platform/edge/contract => ../platform/edge/contract
