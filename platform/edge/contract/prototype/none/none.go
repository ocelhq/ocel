// Package none — PROTOTYPE signatures for the API Gateway front; would live under platform/aws.
package none

import (
	"context"
	"time"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/origin/ledger"
)

type Deps struct {
	APIGateway any
	Ledger     ledger.Dynamo
}

type apigateway struct{ deps Deps }

var _ edge.Edge = (*apigateway)(nil)

func New(deps Deps) edge.Edge { return &apigateway{deps: deps} }

func (a *apigateway) Kind() edge.Kind         { return edge.KindNone }
func (a *apigateway) Supports(edge.Need) bool { return false }
func (a *apigateway) Supported() []edge.Need  { return nil }
func (a *apigateway) FlipBound() edge.FlipBound {
	return edge.FlipBound{Typical: 5 * time.Second, Published: false}
}

func (a *apigateway) Bootstrap(ctx context.Context, class cur.Class) (cur.BootstrapOutput, error) {
	panic("prototype: HTTP API catching the wildcard + ledger table")
}
func (a *apigateway) Teardown(ctx context.Context, class cur.Class) error { panic("prototype") }
func (a *apigateway) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	panic("prototype: domain names + api mappings for spec.Domains")
}
func (a *apigateway) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{state: state, ledger: a.deps.Ledger}, nil
}
func (a *apigateway) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) error {
	panic("prototype: wildcard domain name on the shared HTTP API")
}
func (a *apigateway) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	panic("prototype")
}
func (a *apigateway) DomainOwner(ctx context.Context, hostname string) (string, error) {
	panic("prototype")
}

type stack struct {
	state  edge.StackState
	ledger ledger.Dynamo
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }
func (s *stack) Ledger() edge.Ledger    { return s.ledger }
func (s *stack) Promote(ctx context.Context, promotion cur.Promotion, pointer string) error {
	panic("prototype: ledger.Promote; the router Lambda reads the pointer per request")
}
func (s *stack) RemovePointer(ctx context.Context, pointer string) (cur.PruneResult, error) {
	panic("prototype")
}
func (s *stack) Destroy(ctx context.Context) error { panic("prototype") }
