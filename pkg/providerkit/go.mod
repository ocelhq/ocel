module github.com/ocelhq/ocel/pkg/providerkit

go 1.27.0

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1
	connectrpc.com/connect v1.20.0
	connectrpc.com/validate v0.6.0
	github.com/Microsoft/go-winio v0.6.3-0.20251027160822-ad3df93bed29
	github.com/google/go-containerregistry v0.21.7
	github.com/ocelhq/ocel/pkg/channel v0.0.0
	github.com/ocelhq/ocel/pkg/naming v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0
	github.com/ocelhq/ocel/platform/edge/contract v0.0.0
	golang.org/x/sync v0.22.0
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/go/protovalidate v1.0.0 // indirect
	cel.dev/expr v0.25.2 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.8 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	golang.org/x/exp v0.0.0-20260824195058-e88cd73687aa // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260825221802-da73d73af1c5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260825221802-da73d73af1c5 // indirect
)

replace github.com/ocelhq/ocel/pkg/channel => ../channel

replace github.com/ocelhq/ocel/pkg/naming => ../naming

replace github.com/ocelhq/ocel/pkg/proto => ../proto

replace github.com/ocelhq/ocel/platform/edge/contract => ../../platform/edge/contract
