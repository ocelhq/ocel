// Package cloudflare — PROTOTYPE signatures for platform/edge/cloudflare/deploy under the new contract.
package cloudflare

import (
	"context"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
)

type provider struct{}

var (
	_ edge.Edge               = (*provider)(nil)
	_ edge.Programmable       = (*provider)(nil)
	_ edge.CredentialVerifier = (*provider)(nil)
)

func New() edge.Edge { return &provider{} }

func (p *provider) Kind() edge.Kind { return edge.KindCloudflare }
func (p *provider) Supports(n edge.Need) bool {
	switch n {
	case edge.EdgeMiddleware, edge.EdgeRuntime, edge.PPRResume, edge.EdgeCache:
		return true
	}
	return false
}
func (p *provider) Supported() []edge.Need {
	return []edge.Need{edge.EdgeMiddleware, edge.EdgeRuntime, edge.PPRResume, edge.EdgeCache}
}
func (p *provider) FlipBound() edge.FlipBound { return edge.FlipBound{Instant: true} }

func (p *provider) Bootstrap(ctx context.Context, class cur.Class) (cur.BootstrapOutput, error) {
	panic("prototype")
}
func (p *provider) Teardown(ctx context.Context, class cur.Class) error { panic("prototype") }

func (p *provider) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	panic("prototype: DO instance + generic worker + routes; spec.Program is required here")
}
func (p *provider) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{state: state}, nil
}

func (p *provider) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) error {
	panic("prototype: shared entry worker on *.base/*")
}
func (p *provider) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	panic("prototype")
}
func (p *provider) DomainOwner(ctx context.Context, hostname string) (string, error) {
	panic("prototype: RouteOwner(RoutePattern(hostname))")
}

func (p *provider) AssembleApp(src cur.WorkerSource, r cur.Resolver) (cur.Worker, error) {
	panic("prototype")
}
func (p *provider) DeployApp(ctx context.Context, app cur.AppDeployment) (cur.AppResult, error) {
	panic("prototype")
}
func (p *provider) FindApp(ctx context.Context, name string) (bool, error) { panic("prototype") }
func (p *provider) CodeRuntime() (string, []string)                        { panic("prototype") }
func (p *provider) VerifyCredentials(ctx context.Context) (cur.CredentialIdentity, error) {
	panic("prototype")
}

// The ledger is the Durable Object; a promotion is one DO write, so Promote is a ledger call.
type stack struct{ state edge.StackState }

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }
func (s *stack) Ledger() edge.Ledger    { return (*doLedger)(s) }
func (s *stack) Promote(ctx context.Context, promotion cur.Promotion, pointer string) error {
	panic("prototype: POST /promote")
}
func (s *stack) RemovePointer(ctx context.Context, pointer string) (cur.PruneResult, error) {
	panic("prototype: POST /remove-pointer")
}
func (s *stack) Destroy(ctx context.Context) error {
	panic("prototype: POST /destroy + delete per-app workers under the slug stem")
}

type doLedger stack

var _ edge.Ledger = (*doLedger)(nil)

func (l *doLedger) SchemaVersion(ctx context.Context) (int, error)              { panic("prototype") }
func (l *doLedger) PutStaged(ctx context.Context, r cur.DeploymentRecord) error { panic("prototype") }
func (l *doLedger) History(ctx context.Context, pointer string) ([]cur.HistoryEntry, error) {
	panic("prototype")
}
func (l *doLedger) Prune(ctx context.Context, keepN int, pointer string) (cur.PruneResult, error) {
	panic("prototype")
}
