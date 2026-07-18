// The Cloudflare Workers edge. It lives in its own Go module so cloudflare-go
// stays out of the edge contract's graph and out of any provider that ships a
// native edge instead.
//
// The `replace` pins the edge contract to the local checkout (it has no
// published tag yet); the root go.work wires this for dev.
module github.com/ocelhq/ocel/cloud/edge/cloudflare

go 1.25.11

require (
	github.com/cloudflare/cloudflare-go/v4 v4.6.0
	github.com/ocelhq/ocel/cloud/edge v0.0.0-00010101000000-000000000000
)

require (
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
)

replace github.com/ocelhq/ocel/cloud/edge => ..
