package fake

import (
	"context"
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

type Routers struct {
	edges  *Edges
	planes map[router.Kind]*DataPlane
}

func newRouters(edges *Edges) *Routers {
	routers := &Routers{edges: edges, planes: map[router.Kind]*DataPlane{}}
	for _, kind := range edges.kinds() {
		routers.planes[router.Kind(kind)] = &DataPlane{served: map[projectPointer]map[string]string{}}
	}
	return routers
}

func (e *Edges) pairings() []provider.Pairing {
	kinds := e.kinds()
	pairings := make([]provider.Pairing, 0, len(kinds))
	for _, kind := range kinds {
		pairings = append(pairings, provider.Pairing{Edge: kind, Router: router.Kind(kind), Computes: provider.Computes()})
	}
	return pairings
}

func (r *Routers) Open(kind router.Kind) (router.Router, error) {
	shared := r.edges.Edge(edge.Kind(kind))
	if shared == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the reference provider serves no edge %q; it serves %s", kind, kindList(r.edges.kinds()))
	}
	return Router{edge: shared, plane: r.planes[kind]}, nil
}

func (r *Routers) DataPlane(kind router.Kind) *DataPlane { return r.planes[kind] }

type DataPlane struct {
	mu      sync.Mutex
	failure error
	served  map[projectPointer]map[string]string
}

type projectPointer struct {
	slug    string
	tier    environment.Tier
	pointer string
}

func pointerOf(state edge.StackState, pointer string) projectPointer {
	if pointer == "" {
		pointer = router.DefaultPointer
	}
	return projectPointer{slug: state.Slug, tier: state.Tier, pointer: pointer}
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

func (d *DataPlane) serve(at projectPointer, builds map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.served[at] = maps.Clone(builds)
}

func (d *DataPlane) remove(at projectPointer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.served, at)
}

type Router struct {
	edge  *Edge
	plane *DataPlane
}

func (r Router) Kind() router.Kind { return router.Kind(r.edge.kind) }

func (r Router) Facts() router.Facts { return r.edge.routerFacts() }

func (e *Edge) routerFacts() router.Facts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return router.Facts{
		FlipBound:             router.FlipBound{Typical: 30 * time.Second, Published: true},
		SignsOriginForwards:   true,
		RoutesPreviewsByLabel: e.byLabel,
		ReachesFunctions:      true,
		ReachesContainers:     true,
		Dispatches:            e.kind == KindRelay,
		AnswersHostnames:      true,
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

func (s *RouterStack) Ledger() router.Ledger { return s.stack.ledger }

func (s *RouterStack) Claim(context.Context, string, string) error { return nil }

func (s *RouterStack) Disclaim(context.Context, string) error { return nil }

func (s *RouterStack) Flip(ctx context.Context, flip router.Flip, progress progress.Progress) error {
	if err := flip.RefuseInactive(ctx); err != nil {
		return err
	}
	if err := s.plane.refuseFlip(); err != nil {
		return err
	}
	if err := s.stack.ledger.Promote(ctx, flip.Promotion, flip.Pointer, progress); err != nil {
		return err
	}
	s.plane.serve(pointerOf(s.stack.State(), flip.Pointer), flip.Promotion.Builds)
	return nil
}

func (s *RouterStack) RemovePointer(_ context.Context, pointer string, _ progress.Progress) error {
	s.plane.remove(pointerOf(s.stack.State(), pointer))
	return nil
}

func (s *RouterStack) Destroy(context.Context) error { return nil }
