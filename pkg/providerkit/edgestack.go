package providerkit

import (
	"context"
	"encoding/json"
	"fmt"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type stackStore struct {
	records RecordStore
	name    RecordName
}

func (s stackStore) read(ctx context.Context) (EdgeStackState, error) {
	held, err := Held(ctx, s.records, s.name)
	if err != nil {
		return EdgeStackState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	var state EdgeStackState
	if len(held.Bytes) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(held.Bytes, &state); err != nil {
		return EdgeStackState{}, fmt.Errorf("read %s: %w", s.name, err)
	}
	return state, nil
}

func (s stackStore) write(ctx context.Context, state EdgeStackState) error {
	held, err := Held(ctx, s.records, s.name)
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
	provider Provider
	front    edge.Edge
	stack    edge.EdgeStack
	store    stackStore
	state    EdgeStackState
	settle   settler
}

func (h *handlers) edgeFor(provider Provider, sel *contractv1.EdgeSelection) (edge.Edge, error) {
	kind := edge.Kind(sel.GetKind())
	if kind == "" {
		kind = provider.Edges().Default()
	}
	return provider.Edges().Open(kind)
}

func (h *handlers) dnsFor(provider Provider, sel *contractv1.EdgeSelection) (edge.DNSWriter, error) {
	kind := DNSKind(sel.GetDns().GetKind())
	if kind == "" {
		kind = provider.DNS().Default()
	}
	if kind == "" {
		return nil, nil
	}
	return provider.DNS().Open(kind, sel.GetDns().GetZone())
}

func (h *handlers) openStack(ctx context.Context, class Class, slug string, sel *contractv1.EdgeSelection) (*stackSession, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, Refuse(CodeInvalid, "this call names no project, and an edge stack belongs to one")
	}
	front, err := h.edgeFor(provider, sel)
	if err != nil {
		return nil, err
	}
	store := stackStore{records: provider.Records(), name: EdgeStackRecord(class, slug)}
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
	writer, err := h.dnsFor(provider, sel)
	if err != nil {
		return nil, err
	}
	session := &stackSession{provider: provider, front: front, stack: stack, store: store, state: state}
	session.settle = newSettler(front, writer, sel.GetDns().GetZone(),
		boundBy(front, func() edge.StackState { return session.stack.State() }))
	return session, nil
}

func (s *stackSession) checkpoint(ctx context.Context) error {
	s.state.Edge = s.stack.State()
	return s.store.write(ctx, s.state)
}

func (s *stackSession) on(kind edge.Kind) (edge.EdgeStack, error) {
	front, err := s.provider.Edges().Open(kind)
	if err != nil {
		return nil, err
	}
	return front.Open(s.state.Edge)
}

type noDeploy struct{ Refusal }

func (n noDeploy) Unwrap() error { return n.Refusal }

func errNoDeploy(class Class) error {
	if class == ClassPreview {
		return noDeploy{Refusal{Code: CodeNotReady,
			Message: "this project has no preview deploys yet; run `ocel preview` first"}}
	}
	return noDeploy{Refusal{Code: CodeNotReady,
		Message: "this project has no production deploys yet; run `ocel deploy` first"}}
}
