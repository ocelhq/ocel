// Package native — PROTOTYPE signatures for the CloudFront edge; would live under platform/aws.
package native

import (
	"context"
	"time"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/origin/ledger"
)

// Deps are AWS clients the origin injects; the contract module never sees them.
type Deps struct {
	CloudFront any
	KVS        any
	Ledger     ledger.Dynamo
}

type cloudfront struct{ deps Deps }

var _ edge.Edge = (*cloudfront)(nil)

func New(deps Deps) edge.Edge { return &cloudfront{deps: deps} }

func (c *cloudfront) Kind() edge.Kind           { return edge.KindNative }
func (c *cloudfront) Supports(n edge.Need) bool { return n == edge.EdgeCache }
func (c *cloudfront) Supported() []edge.Need    { return []edge.Need{edge.EdgeCache} }
func (c *cloudfront) FlipBound() edge.FlipBound {
	return edge.FlipBound{Typical: 5 * time.Second, Published: false}
}

func (c *cloudfront) Bootstrap(ctx context.Context, class cur.Class) (cur.BootstrapOutput, error) {
	panic("prototype: wildcard distribution + viewer-request Function + KVS + ledger table")
}
func (c *cloudfront) Teardown(ctx context.Context, class cur.Class) error { panic("prototype") }

func (c *cloudfront) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	panic("prototype: aliases + ACM on the distribution; spec.Program is ignored")
}
func (c *cloudfront) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{state: state, ledger: c.deps.Ledger}, nil
}
func (c *cloudfront) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) error {
	panic("prototype: *.base alias on the wildcard distribution")
}
func (c *cloudfront) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	panic("prototype")
}
func (c *cloudfront) DomainOwner(ctx context.Context, hostname string) (string, error) {
	panic("prototype: alias lookup across distributions")
}

type stack struct {
	state  edge.StackState
	ledger ledger.Dynamo
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }
func (s *stack) Ledger() edge.Ledger    { return s.ledger }
func (s *stack) Promote(ctx context.Context, promotion cur.Promotion, pointer string) error {
	panic("prototype: ledger.Promote then KVS put hostname -> origin")
}
func (s *stack) RemovePointer(ctx context.Context, pointer string) (cur.PruneResult, error) {
	panic("prototype: KVS delete then ledger.RemovePointer")
}
func (s *stack) Destroy(ctx context.Context) error { panic("prototype") }
