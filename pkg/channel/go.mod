// Lean shared module: the private CLI<->plugin local-channel contract (session
// token handshake, Authorization header, readiness sentinel, and address
// encoding) that both the deployment provider and the resource-runtime binaries
// speak. Stdlib-only; no plane owns it.
module github.com/ocelhq/ocel/pkg/channel

go 1.25.11
