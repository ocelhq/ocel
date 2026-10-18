module github.com/ocelhq/ocel/platform/gcp/gateplugin

go 1.27.0

require (
	github.com/ocelhq/ocel/pkg v0.0.0
	github.com/proxy-wasm/proxy-wasm-go-sdk v0.0.0-20260105142703-44c7d5847745
)

require golang.org/x/crypto v0.55.0 // indirect

replace github.com/ocelhq/ocel/pkg => ../../../pkg
