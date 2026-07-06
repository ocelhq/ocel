module github.com/ocelhq/ocel

go 1.25.3

require (
	connectrpc.com/connect v1.20.0
	github.com/evanw/esbuild v0.28.1
	github.com/fsnotify/fsnotify v1.10.1
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/spf13/cobra v1.10.2
	github.com/zalando/go-keyring v0.2.8
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.27.0 // indirect
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	google.golang.org/protobuf/cmd/protoc-gen-go
)
