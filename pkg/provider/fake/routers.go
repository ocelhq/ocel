package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const moveAttempts = 8

type Routers struct {
	mu     sync.Mutex
	edges  *Edges
	planes map[router.Kind]*DataPlane
}

func newRouters(edges *Edges) *Routers {
	routers := &Routers{edges: edges, planes: map[router.Kind]*DataPlane{}}
	for _, kind := range edges.kinds() {
		routers.planes[edges.Edge(kind).routedBy] = &DataPlane{served: map[projectPointer]map[string]string{}, writes: map[projectPointer]int{}, hosts: map[string]projectPointer{}, tables: map[string][]byte{}}
	}
	return routers
}

func (e *Edges) pairings() []provider.Pairing {
	kinds := e.kinds()
	pairings := make([]provider.Pairing, 0, len(kinds))
	for _, kind := range kinds {
		if front := e.Edge(kind); front.pairsStoreRouter() {
			pairings = append(pairings, provider.Pairing{Edge: kind, Router: front.readStoreRouter(), Computes: provider.Computes()})
		}
		pairings = append(pairings, provider.Pairing{Edge: kind, Router: e.Edge(kind).routedBy, Computes: provider.Computes()})
	}
	return pairings
}

func (r *Routers) Open(kind router.Kind) (router.Router, error) {
	var shared *Edge
	for _, front := range r.edges.kinds() {
		paired := r.edges.Edge(front)
		if paired.routedBy == kind || (paired.pairsStoreRouter() && paired.readStoreRouter() == kind) {
			shared = paired
		}
	}
	if shared == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the reference provider has nothing named %q", kind)
	}
	return Router{edge: shared, plane: r.DataPlane(kind), kind: kind}, nil
}

func (r *Routers) DataPlane(kind router.Kind) *DataPlane {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.planes[kind] == nil {
		r.planes[kind] = &DataPlane{served: map[projectPointer]map[string]string{}, writes: map[projectPointer]int{}, hosts: map[string]projectPointer{}}
	}
	return r.planes[kind]
}

type DataPlane struct {
	mu         sync.Mutex
	failure    error
	unremoved  error
	before     func()
	beforeGone func()
	says       string
	progress   progress.Log
	propagates *router.Propagation
	served     map[projectPointer]map[string]string
	writes     map[projectPointer]int
	hosts      map[string]projectPointer
	tables     map[string][]byte
}

type projectPointer struct {
	slug    string
	tier    environment.Tier
	pointer string
}

func pointerOf(state edge.StackState, pointer string) projectPointer {
	return projectPointer{slug: state.Slug, tier: state.Tier, pointer: router.ResolvePointer(pointer)}
}

func (d *DataPlane) Releases(slug string, tier environment.Tier, pointer string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.served[pointerOf(edge.StackState{Slug: slug, Tier: tier}, pointer)])
}

func (d *DataPlane) FindServingPointer(hostname string) (pointer string, releases map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, routed := d.hosts[hostname]
	if !routed {
		return "", nil
	}
	return at.pointer, maps.Clone(d.served[at])
}

func (d *DataPlane) ListServedHostnames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Sorted(maps.Keys(d.hosts))
}

func (d *DataPlane) RouteTable(key string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	table, stored := d.tables[key]
	return slices.Clone(table), stored
}

func (d *DataPlane) ListRouteTableKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Sorted(maps.Keys(d.tables))
}

func (d *DataPlane) storeRouteTable(_ context.Context, _ router.StackState, key string, table []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tables[key] = slices.Clone(table)
	return nil
}

func (d *DataPlane) forgetRouteTable(_ context.Context, _ router.StackState, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tables, key)
	return nil
}

func (d *DataPlane) FailNextPointerMove(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failure = err
}

func (d *DataPlane) FailNextPointerRemoval(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.unremoved = err
}

func (d *DataPlane) BeforeNextPointerMove(before func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.before = before
}

func (d *DataPlane) BeforeNextPointerRemoval(before func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.beforeGone = before
}

func (d *DataPlane) SayOnPointerMove(said string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.says = said
}

func (d *DataPlane) PointerMoveProgress() progress.Log {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.progress
}

func (d *DataPlane) beginPointerMove(progress progress.Log) {
	d.mu.Lock()
	before, said := d.before, d.says
	d.before, d.progress = nil, progress
	d.mu.Unlock()
	if said != "" {
		progress.Say(said)
	}
	if before != nil {
		before()
	}
}

func (d *DataPlane) refusePointerMove() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	failure := d.failure
	d.failure = nil
	if failure == nil {
		return nil
	}
	return router.Unserved{Err: failure}
}

func (d *DataPlane) written(at projectPointer) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writes[at]
}

func (d *DataPlane) serveOver(at projectPointer, releases map[string]string, move router.PointerMove, read int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writes[at] != read {
		return false
	}
	d.served[at] = maps.Clone(releases)
	d.writes[at]++
	d.unrouteHosts(at, move.ListHostsToWithdraw())
	for _, host := range move.Hosts {
		d.hosts[host.Hostname] = at
	}
	return true
}

func (d *DataPlane) unrouteHosts(at projectPointer, hosts []edge.PreviewHost) {
	for _, host := range hosts {
		if d.hosts[host.Hostname] == at {
			delete(d.hosts, host.Hostname)
		}
	}
}

func (d *DataPlane) remove(at projectPointer, hosts []edge.PreviewHost) error {
	d.mu.Lock()
	before := d.beforeGone
	d.beforeGone = nil
	d.mu.Unlock()
	if before != nil {
		before()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if failure := d.unremoved; failure != nil {
		d.unremoved = nil
		return failure
	}
	delete(d.served, at)
	d.writes[at]++
	d.unrouteHosts(at, hosts)
	return nil
}

type Router struct {
	edge  *Edge
	plane *DataPlane
	kind  router.Kind
}

func (r Router) Kind() router.Kind { return r.kind }

func (r Router) Facts() router.Facts {
	facts := r.edge.routerFacts()
	if propagation, set := r.plane.propagation(); set {
		facts.Propagation = propagation
	}
	return facts
}

func (r Router) Hooks() router.Hooks {
	if r.kind != r.edge.routedBy {
		return router.Hooks{}
	}
	hooks := router.Hooks{Origin: &router.OriginHooks{
		PlanProjectRemoval:      r.planProjectRemoval,
		ClaimPreviewEntry:       r.claimPreviewEntry,
		DisclaimPreviewEntry:    r.disclaimPreviewEntry,
		PlanPreviewEntryRemoval: r.planPreviewEntryRemoval,
	}}
	if r.edge.kind == KindRelay {
		hooks.RouteTables = &router.RouteTableHooks{Store: r.plane.storeRouteTable, Forget: r.plane.forgetRouteTable}
	}
	return hooks
}

func (e *Edge) routerFacts() router.Facts {
	e.mu.Lock()
	defer e.mu.Unlock()
	supported := edge.AllNeeds()
	if e.routerServes != nil {
		supported = slices.Clone(*e.routerServes)
	}
	return router.Facts{
		Supported:                   supported,
		Propagation:                 router.Propagation{Typical: 30 * time.Second, Published: true},
		SignsOriginForwards:         true,
		AddressesItself:             e.addressesItself,
		ReachesFunctions:            true,
		ReachesContainers:           true,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
		ServesPreviewDeployments:    true,
	}
}

const ClaimKind = "Fake::Claim"

func (r Router) planProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
	held := r.edge.listHeldClaims()
	var changes []edge.PlanChange
	for _, hostname := range scope.Hostnames {
		if slices.Contains(held, hostname) {
			changes = append(changes, edge.PlanChange{Kind: ClaimKind, Name: hostname, Action: edge.PlanDelete})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.OriginGroupName,
		Action:  edge.PlanDelete,
		Changes: changes,
	}}
}

func (r Router) claimPreviewEntry(_ context.Context, claim router.Claim) (edge.Origin, error) {
	if !strings.HasPrefix(claim.Hostname, "*.") {
		return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid, "a preview entry is a wildcard, and %q is none", claim.Hostname)
	}
	return r.edge.recordPreviewEntryClaim(claim)
}

func (r Router) disclaimPreviewEntry(_ context.Context, baseDomain string) error {
	r.edge.recordPreviewEntryDisclaim(baseDomain)
	return nil
}

const PreviewEntryClaimKind = "Fake::PreviewEntryClaim"

func (r Router) planPreviewEntryRemoval(wildcard string) []edge.PlanGroup {
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.OriginGroupName,
		Action:  edge.PlanDelete,
		Changes: []edge.PlanChange{{Kind: PreviewEntryClaimKind, Name: wildcard, Action: edge.PlanDelete}},
	}}
}

func (r Router) Reconcile(_ context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	return r.Open(prior.WithSpec(spec))
}

func (r Router) Open(state router.StackState) (router.Stack, error) {
	shared, err := r.edge.open(state.Edge)
	if err != nil {
		return nil, err
	}
	return &RouterStack{stack: shared, plane: r.plane}, nil
}

type RouterStack struct {
	stack *Stack
	plane *DataPlane
}

func (s *RouterStack) State() router.StackState {
	state := s.stack.State()
	return router.NewStackState(state)
}

func (s *RouterStack) Claim(_ context.Context, claim router.Claim) (edge.Origin, error) {
	return s.stack.front.recordClaim(claim)
}

func (s *RouterStack) Disclaim(_ context.Context, hostname string) error {
	return s.stack.front.recordDisclaim(hostname)
}

func (s *RouterStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	s.plane.beginPointerMove(progress)
	releases := make(map[string]string, len(move.Records))
	for app, record := range move.Records {
		releases[app] = record.Release
	}
	at := pointerOf(s.stack.State(), move.Pointer)
	for range moveAttempts {
		read := s.plane.written(at)
		if err := move.RefuseInactive(ctx); err != nil {
			return err
		}
		if err := s.plane.refusePointerMove(); err != nil {
			return err
		}
		if s.plane.serveOver(at, releases, move, read) {
			return nil
		}
	}
	return router.Unserved{Err: fmt.Errorf("move %s onto %s: another promotion moved the pointer on every one of %d attempts", move.Promotion.PromotionID, at.pointer, moveAttempts)}
}

func (s *RouterStack) RemovePointer(_ context.Context, removal router.PointerRemoval, _ progress.Log) error {
	return s.plane.remove(pointerOf(s.stack.State(), removal.Pointer), removal.Hosts)
}

func (s *RouterStack) Destroy(context.Context) error { return nil }
