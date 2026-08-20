package fake

import (
	"context"
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Edge struct {
	Name  edge.Kind
	Runs  bool
	Front string
}

func (e Edge) Kind() edge.Kind { return e.Name }
func (e Edge) Facts() edge.Facts {
	return edge.Facts{RunsCode: e.Runs, ServesUnbound: e.Runs, SignsOriginForwards: e.Runs}
}
func (e Edge) Supported() []edge.Need {
	if e.Runs {
		return edge.AllNeeds()
	}
	return []edge.Need{edge.NeedStreaming}
}
func (e Edge) FlipBound() edge.FlipBound { return edge.FlipBound{} }
func (e Edge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}
func (e Edge) Teardown(context.Context, edge.Class) error { return nil }
func (e Edge) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	st := prior
	st.Slug, st.Class = spec.Slug, spec.Class
	if st.Front == "" {
		st.Front = fmt.Sprintf("%s.%s.%s", spec.Slug, e.Name, e.Front)
	}
	return &stack{state: st, ledger: ledgerFor(e.Name, st)}, nil
}
func (e Edge) Open(state edge.StackState) (edge.EdgeStack, error) {
	return &stack{state: state, ledger: ledgerFor(e.Name, state)}, nil
}

var ledgers = map[string]*ledger{}

func ledgerFor(kind edge.Kind, st edge.StackState) *ledger {
	k := fmt.Sprintf("%s/%s/%s", kind, st.Slug, st.Class)
	if ledgers[k] == nil {
		ledgers[k] = &ledger{}
	}
	return ledgers[k]
}
func (e Edge) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	return "*." + spec.BaseDomain, nil
}
func (e Edge) DestroyPreviewWildcard(context.Context, string) error        { return nil }
func (e Edge) DomainOwner(context.Context, string) (string, error)         { return "", nil }
func (e Edge) ProjectSurfaces(edge.ProjectScope) []edge.Surface            { return nil }
func (e Edge) PreviewWildcardSurfaces(string) (removed, kept edge.Surface) { return }
func (e Edge) SharedPreviewSurface() edge.Surface                          { return edge.Surface{} }

type stack struct {
	state  edge.StackState
	ledger *ledger
}

func (s *stack) State() edge.StackState { return s.state }
func (s *stack) Ledger() edge.Ledger    { return s.ledger }
func (s *stack) Promote(_ context.Context, p edge.Promotion, _ string) error {
	s.ledger.history = append([]edge.HistoryEntry{{Promotion: p, Active: true}}, s.ledger.history...)
	for i := range s.ledger.history[1:] {
		s.ledger.history[i+1].Active = false
	}
	return nil
}
func (s *stack) RemovePointer(context.Context, string) (edge.PruneResult, error) {
	return edge.PruneResult{}, nil
}
func (s *stack) BindDomain(_ context.Context, b edge.DomainBinding) error {
	s.state.Bind(b.Hostname)
	return nil
}
func (s *stack) UnbindDomain(_ context.Context, h string) error {
	s.state.Release(h)
	return nil
}
func (s *stack) Destroy(context.Context) error { return nil }

type ledger struct {
	staged  []edge.DeploymentRecord
	history []edge.HistoryEntry
}

func (l *ledger) SchemaVersion(context.Context) (int, error) { return edge.StoreSchemaVersion, nil }
func (l *ledger) PutStaged(_ context.Context, r edge.DeploymentRecord) error {
	l.staged = append(l.staged, r)
	return nil
}
func (l *ledger) History(context.Context, string) ([]edge.HistoryEntry, error) {
	return l.history, nil
}
func (l *ledger) Prune(_ context.Context, keep int, _ string) (edge.PruneResult, error) {
	var out edge.PruneResult
	for i, h := range l.history {
		if i < keep {
			out.KeptPromotionIDs = append(out.KeptPromotionIDs, h.PromotionID)
		} else {
			out.RemovedPromotionIDs = append(out.RemovedPromotionIDs, h.PromotionID)
		}
	}
	if len(l.history) > keep {
		l.history = l.history[:keep]
	}
	return out, nil
}
