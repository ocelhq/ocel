module github.com/ocelhq/ocel/sdk

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/ocelhq/ocel/pkg/constants v0.0.0
	github.com/ocelhq/ocel/pkg/proto v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260709200747-435963d16310.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/ocelhq/ocel/pkg/proto => ../pkg/proto

replace github.com/ocelhq/ocel/pkg/constants => ../pkg/constants
