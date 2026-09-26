package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type stackStore struct {
	records records.Store
	name    records.Name
}

func (s stackStore) read(ctx context.Context) (stackrecords.EdgeState, error) {
	held, err := records.ReadOrEmpty(ctx, s.records, s.name)
	if err != nil {
		return stackrecords.EdgeState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	var state stackrecords.EdgeState
	if len(held.Bytes) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(held.Bytes, &state); err != nil {
		return stackrecords.EdgeState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	return state, nil
}

func (s stackStore) write(ctx context.Context, state stackrecords.EdgeState) error {
	held, err := records.ReadOrEmpty(ctx, s.records, s.name)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.name, err)
	}
	if held.Bytes, err = json.Marshal(state); err != nil {
		return fmt.Errorf("record %s: %w", s.name, err)
	}
	if _, err := s.records.Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", s.name, err)
	}
	return nil
}

type stackSession struct {
	provider provider.Provider
	front    edge.Edge
	stack    edge.EdgeStack
	store    stackStore
	state    stackrecords.EdgeState
	settle   settlement
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

func (h *handlers) openStack(ctx context.Context, class edge.Class, slug string, sel *contractv1.EdgeSelection) (*stackSession, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "this call names no project, and an edge stack belongs to one")
	}
	front, err := h.edgeFor(provider, sel)
	if err != nil {
		return nil, err
	}
	store := stackStore{records: provider.Records(), name: stackrecords.EdgeStackRecord(class, slug)}
	state, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	if state.Edge.Empty() {
		return nil, errNoDeploy(class)
	}
	stack, err := front.Open(state.Edge)
	if err != nil {
		return nil, err
	}
	writer, err := dnsFor(provider, front, sel)
	if err != nil {
		return nil, err
	}
	session := &stackSession{provider: provider, front: front, stack: stack, store: store, state: state}
	session.installSettler(writer, sel.GetDns().GetZone())
	return session, nil
}

func (s *stackSession) installSettler(writer edge.DNSRecords, zone string) {
	s.settle = newSettlement(s.front, writer, zone, s.provider.Liveness())
}

func (s *stackSession) checkpoint(ctx context.Context) error {
	s.state.Kind = s.front.Kind()
	s.state.Edge = s.stack.State()
	return s.store.write(ctx, s.state)
}

func (s *stackSession) promoted(ctx context.Context) (bool, error) {
	history, err := s.stack.Ledger().History(ctx, edge.DefaultPointer)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(history, func(entry edge.HistoryEntry) bool { return entry.Active }), nil
}

func (s *stackSession) on(kind edge.Kind) (edge.EdgeStack, error) {
	front, err := s.provider.Edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return front.Open(s.state.Edge)
}

type noDeploy struct{ refusal.Refusal }

func (n noDeploy) Unwrap() error { return n.Refusal }

func errNoDeploy(class edge.Class) error {
	if class == edge.ClassPreview {
		return noDeploy{refusal.Refusal{Code: refusal.CodeNotReady,
			Message: "this project has no preview deploys yet; run `ocel preview` first"}}
	}
	return noDeploy{refusal.Refusal{Code: refusal.CodeNotReady,
		Message: "this project has no production deploys yet; run `ocel deploy` first"}}
}
