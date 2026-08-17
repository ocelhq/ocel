package edge

import (
	"context"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
)

// Optional method sets: control-plane operations only some edges have.
// Presence must agree with Supports — an edge that Supports(EdgeMiddleware) or
// Supports(EdgeRuntime) implements Programmable; one that does not, does not.
// The origin discovers them by type assertion; the contract test enforces the pairing.

// Programmable is an edge that runs the entry program and per-app edge code.
// An edge without it routes nothing: the origin fronts itself with its own router.
type Programmable interface {
	AssembleApp(src cur.WorkerSource, r cur.Resolver) (cur.Worker, error)
	DeployApp(ctx context.Context, app cur.AppDeployment) (cur.AppResult, error)
	FindApp(ctx context.Context, name string) (bool, error)
	CodeRuntime() (compatDate string, compatFlags []string)
}

type CredentialVerifier interface {
	VerifyCredentials(ctx context.Context) (cur.CredentialIdentity, error)
}
