package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type edgeStateStore struct {
	keyValues keyvalue.Store
	name      keyvalue.Key
}

func (s edgeStateStore) read(ctx context.Context) (stackrecords.EdgeState, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, s.keyValues, s.name)
	if err != nil {
		return stackrecords.EdgeState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	var state stackrecords.EdgeState
	if len(recorded.Value) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(recorded.Value, &state); err != nil {
		return stackrecords.EdgeState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	return state, nil
}

func (s edgeStateStore) write(ctx context.Context, state stackrecords.EdgeState) error {
	recorded, err := keyvalue.ReadOrEmpty(ctx, s.keyValues, s.name)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.name, err)
	}
	if recorded.Value, err = json.Marshal(state); err != nil {
		return fmt.Errorf("record %s: %w", s.name, err)
	}
	if _, err := s.keyValues.Write(ctx, recorded); err != nil {
		return fmt.Errorf("record %s: %w", s.name, err)
	}
	return nil
}

type edgeSession struct {
	sharedStack
	provider provider.Provider
	store    edgeStateStore
	state    stackrecords.EdgeState
	cutover  dnsCutover
}

func (h *handlers) edgeFor(p provider.Provider, sel *contractv1.EdgeSelection) (edge.Edge, error) {
	kind := edge.Kind(sel.GetKind())
	if kind == "" {
		kind = p.Facts().DefaultEdge
	}
	return p.Edges().Open(kind)
}

func (h *handlers) removalEdge(p provider.Provider, state stackrecords.EdgeState, sel *contractv1.EdgeSelection) (edge.Edge, error) {
	if state.Kind == "" {
		return h.edgeFor(p, sel)
	}
	return p.Edges().Open(state.Kind)
}

func dnsFor(p provider.Provider, front edge.Edge, sel *contractv1.EdgeSelection) (edge.DNSRecords, error) {
	kind := provider.DNSKind(sel.GetDns().GetKind())
	if kind == "" {
		return nil, nil
	}
	return p.DNS().Open(kind, sel.GetDns().GetZone(), front.Kind())
}

func (h *handlers) openEdgeSession(ctx context.Context, tier environment.Tier, slug string, sel *contractv1.EdgeSelection) (*edgeSession, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "this call names no project, and an edge stack belongs to one")
	}
	front, err := h.edgeFor(vendor, sel)
	if err != nil {
		return nil, err
	}
	store := edgeStateStore{keyValues: vendor.KeyValues(), name: stackrecords.EdgeStackKey(tier, slug)}
	state, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	if state.Edge.Empty() {
		return nil, errNoDeploy(tier)
	}
	stack, err := front.Open(state.Edge)
	if err != nil {
		return nil, err
	}
	paired, err := sharedRouter(vendor, front)
	if err != nil {
		return nil, err
	}
	writer, err := dnsFor(vendor, front, sel)
	if err != nil {
		return nil, err
	}
	session := &edgeSession{
		sharedStack: sharedStack{front: front, router: paired, stack: stack},
		provider:    vendor, store: store, state: state,
	}
	session.installDNSCutover(writer, sel.GetDns().GetZone())
	return session, nil
}

func (s *edgeSession) installDNSCutover(writer edge.DNSRecords, zone string) {
	s.cutover = newDNSCutover(s.front, writer, zone, s.provider.Liveness())
}

func (s *edgeSession) checkpoint(ctx context.Context) error {
	s.state.Kind = s.front.Kind()
	s.state.Edge = s.stack.State()
	return s.store.write(ctx, s.state)
}

func (s *edgeSession) promoted(ctx context.Context) (bool, error) {
	history, err := s.readHistory(ctx, router.DefaultPointer)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(history, func(entry router.HistoryEntry) bool { return entry.Active }), nil
}

func (s *edgeSession) on(kind edge.Kind) (edge.EdgeStack, error) {
	front, err := s.provider.Edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return front.Open(s.state.Edge)
}

type noDeploy struct{ refusal.Refusal }

func (n noDeploy) Unwrap() error { return n.Refusal }

func errNoDeploy(tier environment.Tier) error {
	if tier == environment.TierPreview {
		return noDeploy{refusal.Refusal{Code: refusal.CodeNotReady,
			Message: "this project has no preview deploys yet; run `ocel preview` first"}}
	}
	return noDeploy{refusal.Refusal{Code: refusal.CodeNotReady,
		Message: "this project has no production deploys yet; run `ocel deploy` first"}}
}
