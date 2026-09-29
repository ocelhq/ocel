package fake

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const flipAttempts = 8

type Routers struct {
	edges  *Edges
	planes map[router.Kind]*DataPlane
}

func newRouters(edges *Edges) *Routers {
	routers := &Routers{edges: edges, planes: map[router.Kind]*DataPlane{}}
	for _, kind := range edges.kinds() {
		routers.planes[edges.Edge(kind).routedBy] = &DataPlane{served: map[projectPointer]map[string]string{}, writes: map[projectPointer]int{}}
	}
	return routers
}

func (e *Edges) pairings() []provider.Pairing {
	kinds := e.kinds()
	pairings := make([]provider.Pairing, 0, len(kinds))
	for _, kind := range kinds {
		pairings = append(pairings, provider.Pairing{Edge: kind, Router: e.Edge(kind).routedBy, Computes: provider.Computes()})
	}
	return pairings
}

func (r *Routers) Open(kind router.Kind) (router.Router, error) {
	var shared *Edge
	for _, front := range r.edges.kinds() {
		if paired := r.edges.Edge(front); paired.routedBy == kind {
			shared = paired
		}
	}
	if shared == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the reference provider has nothing named %q", kind)
	}
	return Router{edge: shared, plane: r.planes[kind]}, nil
}

func (r *Routers) DataPlane(kind router.Kind) *DataPlane { return r.planes[kind] }

type DataPlane struct {
	mu       sync.Mutex
	failure  error
	before   func()
	says     string
	progress progress.Progress
	served   map[projectPointer]map[string]string
	writes   map[projectPointer]int
}

type projectPointer struct {
	slug    string
	tier    environment.Tier
	pointer string
}

func pointerOf(state edge.StackState, pointer string) projectPointer {
	return projectPointer{slug: state.Slug, tier: state.Tier, pointer: router.ResolvePointer(pointer)}
}

func (d *DataPlane) Builds(slug string, tier environment.Tier, pointer string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.served[pointerOf(edge.StackState{Slug: slug, Tier: tier}, pointer)])
}

func (d *DataPlane) FailNextFlip(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failure = err
}

func (d *DataPlane) BeforeNextFlip(before func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.before = before
}

func (d *DataPlane) SayOnFlip(said string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.says = said
}

func (d *DataPlane) FlipProgress() progress.Progress {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.progress
}

func (d *DataPlane) beginFlip(progress progress.Progress) {
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

func (d *DataPlane) refuseFlip() error {
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

func (d *DataPlane) serveOver(at projectPointer, builds map[string]string, read int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writes[at] != read {
		return false
	}
	d.served[at] = maps.Clone(builds)
	d.writes[at]++
	return true
}

func (d *DataPlane) remove(at projectPointer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.served, at)
	d.writes[at]++
}

type Router struct {
	edge  *Edge
	plane *DataPlane
}

func (r Router) Kind() router.Kind { return r.edge.routedBy }

func (r Router) Facts() router.Facts { return r.edge.routerFacts() }

func (e *Edge) routerFacts() router.Facts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return router.Facts{
		FlipBound:                   router.FlipBound{Typical: 30 * time.Second, Published: true},
		SignsOriginForwards:         true,
		RoutesPreviewsByLabel:       e.byLabel,
		ReachesFunctions:            true,
		ReachesContainers:           true,
		Dispatches:                  e.kind == KindRelay,
		AnswersHostnames:            true,
		StopsServingRemovedPointers: true,
	}
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
	return s.stack.front.claimed(claim), nil
}

func (s *RouterStack) Disclaim(_ context.Context, hostname string) error {
	s.stack.front.gaveBack(hostname)
	return nil
}

func (s *RouterStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	s.plane.beginFlip(progress)
	builds := make(map[string]string, len(flip.Records))
	for app, record := range flip.Records {
		builds[app] = record.Build
	}
	at := pointerOf(s.stack.State(), flip.Pointer)
	for range flipAttempts {
		read := s.plane.written(at)
		if err := flip.RefuseInactive(ctx); err != nil {
			return err
		}
		if err := s.plane.refuseFlip(); err != nil {
			return err
		}
		if s.plane.serveOver(at, builds, read) {
			return nil
		}
	}
	return router.Unserved{Err: fmt.Errorf("flip %s onto %s: another flip moved it on every one of %d attempts", flip.Promotion.PromotionID, at.pointer, flipAttempts)}
}

func (s *RouterStack) RemovePointer(_ context.Context, pointer string, _ progress.Progress) error {
	s.plane.remove(pointerOf(s.stack.State(), pointer))
	return nil
}

func (s *RouterStack) Destroy(context.Context) error { return nil }
