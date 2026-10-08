package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
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
	*sharedStack
	provider provider.Provider
	store    edgeStateStore
	state    stackrecords.EdgeState
	cutover  dnsCutover
	tunnel   edge.Kind
}

func (h *handlers) edgeFor(p provider.Provider, sel *contractv1.EdgeSelection) (edge.Edge, error) {
	kind := edge.Kind(sel.GetKind())
	if kind == "" {
		kind = p.Facts().DefaultEdge
	}
	front, err := p.Edges().Open(kind, sel.GetOptions().AsMap())
	if err != nil || !front.Facts().TunnelsToOrigin {
		return front, err
	}
	if !p.Facts().RunsTunnels {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s is set to reach its origin through a tunnel, and no origin of this provider runs one: change the edge's options so it forwards to the origin's address", describeFront(kind))
	}
	if front.Hooks().Tunnels == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s is set to reach its origin through a tunnel, and it opens no tunnel to an origin: change the edge's options so it forwards to the origin's address", describeFront(kind))
	}
	return front, nil
}

func readTunnelToOrigin(front edge.Edge) edge.Kind {
	if !front.Facts().TunnelsToOrigin {
		return edge.None
	}
	return front.Kind()
}

func (h *handlers) removalEdge(p provider.Provider, state stackrecords.EdgeState, sel *contractv1.EdgeSelection) (edge.Edge, error) {
	if state.Edge.Empty() {
		return h.edgeFor(p, sel)
	}
	return p.Edges().Open(state.Kind, nil)
}

func describeFront(kind edge.Kind) string {
	if kind == edge.None {
		return "the origin"
	}
	return "the " + string(kind) + " edge"
}

func dnsFor(p provider.Provider, front edge.Edge, sel *contractv1.EdgeSelection) (edge.DNSRecords, error) {
	kind := provider.DNSKind(sel.GetDns().GetKind())
	if kind == "" {
		return nil, nil
	}
	return p.DNS().Open(kind, sel.GetDns().GetZone(), front.Kind())
}

var errUnnamedProject = refusal.Refuse(refusal.CodeInvalid, "this call names no project, and what it changes belongs to one")

func (h *handlers) openEdgeSession(ctx context.Context, tier environment.Tier, slug string, sel *contractv1.EdgeSelection) (*edgeSession, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, errUnnamedProject
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
	shared, err := openSharedStack(vendor, front, tier, slug)
	if err != nil {
		return nil, err
	}
	writer, err := dnsFor(vendor, front, sel)
	if err != nil {
		return nil, err
	}
	shared.setEdgeStack(stack)
	shared.restoreRouterStates(state)
	session := &edgeSession{sharedStack: shared, provider: vendor, store: store, state: state, tunnel: readTunnelToOrigin(front)}
	session.installDNSCutover(writer, sel.GetDns().GetZone())
	return session, nil
}

func (s *edgeSession) installDNSCutover(writer edge.DNSRecords, zone string) {
	s.cutover = newDNSCutover(s.front, writer, zone, s.provider.Liveness())
}

func (s *edgeSession) checkpoint(ctx context.Context) error {
	s.state.Kind = s.front.Kind()
	s.state.Edge = s.edgeStack().State()
	s.recordRouterStates(&s.state)
	return s.store.write(ctx, s.state)
}

func (s *edgeSession) releasePointerHostnames(ctx context.Context, pointer string, runProgress progress.Log) error {
	for _, hostname := range s.state.PointerHostnames(pointer) {
		runProgress.Say(fmt.Sprintf("Unbinding %s from %s", hostname, describeFront(s.front.Kind())))
		if err := progress.ReportWarning(runProgress, s.edgeStack().UnbindDomain(ctx, hostname)); err != nil {
			return err
		}
		if kind := s.state.Host(hostname).Router; kind != "" {
			if err := (&hostnames{edgeSession: s}).disclaim(ctx, hostname, kind); err != nil {
				return err
			}
		}
		s.state.Forget(hostname)
	}
	return nil
}

func (s *edgeSession) promoted(ctx context.Context) (bool, error) {
	active, err := s.ledger.ActivePromotionID(ctx, router.DefaultPointer)
	return active != "", err
}

func (s *edgeSession) readAppRouter(app string) (router.Kind, error) {
	kind, paired := s.state.Apps[app]
	if !paired {
		return "", fmt.Errorf("the edge state pairs app %s with no router", app)
	}
	return kind, nil
}

func (s *edgeSession) readTargetRouter(target ConfiguredHost) (router.Kind, error) {
	if target.App == "" {
		return s.edgeKind, nil
	}
	return s.readAppRouter(target.App)
}

func (s *edgeSession) recordRouterStates(into *stackrecords.EdgeState) {
	for _, kind := range slices.Sorted(maps.Keys(into.Routers)) {
		paired, found := s.routers[kind]
		if !found {
			delete(into.Routers, kind)
			continue
		}
		into.Routers[kind] = paired.record()
	}
}

func (s *edgeSession) on(kind edge.Kind) (edge.EdgeStack, error) {
	front, err := s.provider.Edges().Open(kind, nil)
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

func (s *edgeSession) removeDeployments(ctx context.Context, deployments []router.PointerRemoval, runProgress progress.Log) error {
	released := false
	for _, deployment := range deployments {
		if len(s.state.PointerHostnames(deployment.Pointer)) == 0 {
			continue
		}
		if err := s.releasePointerHostnames(ctx, deployment.Pointer, runProgress); err != nil {
			return err
		}
		released = true
	}
	if released {
		if err := s.checkpoint(ctx); err != nil {
			return err
		}
	}
	return s.sharedStack.removeDeployments(ctx, deployments, runProgress)
}
