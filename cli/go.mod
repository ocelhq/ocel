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
)

require google.golang.org/protobuf v1.36.11 // indirect

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
	github.com/ocelhq/ocel/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/pelletier/go-toml v1.9.5 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/spf13/afero v1.14.0 // indirect
	github.com/spf13/cast v1.9.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tdewolff/parse/v2 v2.8.3 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/term v0.1.0 // indirect
	golang.org/x/text v0.28.0 // indirect
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	github.com/air-verse/air
	google.golang.org/protobuf/cmd/protoc-gen-go
)

replace github.com/ocelhq/ocel/pkg/channel => ../pkg/channel

replace github.com/ocelhq/ocel/pkg/proto => ../pkg/proto
