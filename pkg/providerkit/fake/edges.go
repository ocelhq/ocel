package fake

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	KindRelay  edge.Kind = "relay"
	KindDirect edge.Kind = "direct"
)

type Ledger interface {
	edge.Ledger

	Promote(ctx context.Context, promotion edge.Promotion, pointer string) error

	RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error)

	Destroy(ctx context.Context) error
}

type Edges struct {
	mu    sync.Mutex
	order []edge.Kind
	edges map[edge.Kind]*Edge
}

func NewEdges(records providerkit.RecordStore) *Edges {
	registry := &Edges{edges: map[edge.Kind]*Edge{}}
	for _, kind := range []edge.Kind{KindRelay, KindDirect} {
		registry.order = append(registry.order, kind)
		registry.edges[kind] = newEdge(kind, records)
	}
	return registry
}

func (e *Edges) Supported() []edge.Kind {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.order)
}

func (e *Edges) Default() edge.Kind { return KindRelay }

func (e *Edges) Open(kind edge.Kind) (edge.Edge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	front, served := e.edges[kind]
	if !served {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the reference provider serves no edge %q; it serves %s", kind, kindList(e.order))
	}
	return front, nil
}

func (e *Edges) Edge(kind edge.Kind) *Edge {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.edges[kind]
}

func kindList(kinds []edge.Kind) string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return strings.Join(names, ", ")
}

type Edge struct {
	mu       sync.Mutex
	kind     edge.Kind
	records  providerkit.RecordStore
	ledgers  func(edge.StackState) Ledger
	owners   map[string]string
	wildcard string
	specs    []edge.PreviewWildcardSpec
	bindings []edge.DomainBinding
	serves   *[]edge.Need
	refusal  error
}

func (e *Edge) Bindings() []edge.DomainBinding {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.bindings)
}

func (e *Edge) bound(binding edge.DomainBinding) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bindings = append(e.bindings, binding)
}

func newEdge(kind edge.Kind, records providerkit.RecordStore) *Edge {
	return &Edge{kind: kind, records: records, owners: map[string]string{}}
}

func (e *Edge) UseLedger(ledgers func(edge.StackState) Ledger) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ledgers = ledgers
}

func (e *Edge) Refuse(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refusal = err
}

func (e *Edge) Owns(hostname, owner string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.owners[hostname] = owner
}

func (e *Edge) Wildcard() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.wildcard
}

func (e *Edge) Specs() []edge.PreviewWildcardSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.specs)
}

func (e *Edge) Kind() edge.Kind { return e.kind }

func (e *Edge) Facts() edge.Facts {
	return edge.Facts{
		RunsCode:            e.kind == KindRelay,
		SignsOriginForwards: true,
		CredentialScope:     "fake-account",
	}
}

func (e *Edge) Serves(needs []edge.Need) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.serves = &needs
}

func (e *Edge) Supported() []edge.Need {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.serves != nil {
		return slices.Clone(*e.serves)
	}
	return edge.AllNeeds()
}

func (e *Edge) FlipBound() edge.FlipBound {
	return edge.FlipBound{Typical: 30 * time.Second, Published: true}
}

func (e *Edge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (e *Edge) Teardown(context.Context, edge.Class) error { return nil }

func (e *Edge) Reconcile(_ context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	state := prior
	state.Slug, state.Class = spec.Slug, spec.Class
	state.Front = e.front(spec.Slug)
	return e.open(state)
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) { return e.open(state) }

func (e *Edge) open(state edge.StackState) (*Stack, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusal != nil {
		return nil, e.refusal
	}
	if state.Front == "" {
		state.Front = e.front(state.Slug)
	}
	build := e.ledgers
	if build == nil {
		records := e.records
		build = func(held edge.StackState) Ledger {
			return ledger.New(records, providerkit.Class(held.Class), held.Slug)
		}
	}
	return &Stack{front: e, state: state, ledger: build(state)}, nil
}

func (e *Edge) front(slug string) string {
	return slug + "." + string(e.kind) + ".fake.invalid"
}

func (e *Edge) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusal != nil {
		return "", e.refusal
	}
	e.wildcard = spec.BaseDomain
	e.specs = append(e.specs, spec)
	e.owners[edge.PreviewWildcard(spec.BaseDomain)] = edge.PreviewEntryOwner
	return "preview." + string(e.kind) + ".fake.invalid", nil
}

func (e *Edge) DestroyPreviewWildcard(_ context.Context, baseDomain string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusal != nil {
		return e.refusal
	}
	e.wildcard = ""
	delete(e.owners, edge.PreviewWildcard(baseDomain))
	return nil
}

func (e *Edge) DomainOwner(_ context.Context, hostname string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owners[hostname], nil
}

func (e *Edge) ProjectSurfaces(scope edge.ProjectScope) []edge.Surface {
	surfaces := []edge.Surface{{
		Kind:   "edge stack",
		Name:   scope.Slug + "-" + string(scope.Class),
		Action: edge.SurfaceDelete,
		Reason: "the " + string(e.kind) + " edge stack this project deploys through",
	}}
	for _, hostname := range scope.Hostnames {
		surfaces = append(surfaces, edge.Surface{
			Kind:   "hostname",
			Name:   hostname,
			Action: edge.SurfaceDelete,
			Reason: "bound to this project's edge stack",
		})
	}
	return surfaces
}

func (e *Edge) PreviewWildcardSurfaces(wildcard string) (removed, kept edge.Surface) {
	return edge.Surface{
			Kind:   "preview entry",
			Name:   wildcard,
			Action: edge.SurfaceDelete,
			Reason: "the shared entry every preview on this wildcard is served through",
		}, edge.Surface{
			Kind:   "preview entry worker",
			Name:   edge.PreviewEntryOwner,
			Action: edge.SurfaceKeep,
			Reason: "shared with every other wildcard in this account",
		}
}

func (e *Edge) SharedPreviewSurface() edge.Surface {
	return edge.Surface{
		Kind:   "preview entry worker",
		Name:   edge.PreviewEntryOwner,
		Action: edge.SurfaceKeep,
		Reason: "shared with every other wildcard in this account",
	}
}

type Stack struct {
	front  *Edge
	mu     sync.Mutex
	state  edge.StackState
	ledger Ledger
}

func (s *Stack) State() edge.StackState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Stack) Ledger() edge.Ledger { return s.ledger }

func (s *Stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
	return s.ledger.Promote(ctx, promotion, pointer)
}

func (s *Stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	return s.ledger.RemovePointer(ctx, pointer)
}

func (s *Stack) BindDomain(_ context.Context, binding edge.DomainBinding) error {
	s.front.bound(binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, s.front.front(s.state.Slug))
	return nil
}

func (s *Stack) UnbindDomain(_ context.Context, hostname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *Stack) Destroy(ctx context.Context) error {
	s.mu.Lock()
	s.state = edge.StackState{}
	s.mu.Unlock()
	return s.ledger.Destroy(ctx)
}

type DNS struct {
	mu      sync.Mutex
	writers map[string]*DNSWriter
}

const KindZone providerkit.DNSKind = "zone"

func NewDNS() *DNS { return &DNS{writers: map[string]*DNSWriter{}} }

func (d *DNS) Supported() []providerkit.DNSKind { return []providerkit.DNSKind{KindZone} }

func (d *DNS) Default() providerkit.DNSKind { return "" }

func (d *DNS) Open(kind providerkit.DNSKind, zone string) (edge.DNSWriter, error) {
	if kind != KindZone {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the reference provider writes no dns %q; it writes %s", kind, KindZone)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	writer, open := d.writers[zone]
	if !open {
		writer = &DNSWriter{zone: zone}
		d.writers[zone] = writer
	}
	return writer, nil
}

func (d *DNS) Writer(zone string) *DNSWriter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writers[zone]
}

type DNSWriter struct {
	mu      sync.Mutex
	zone    string
	records []edge.Record
	refusal error
}

func (w *DNSWriter) Refuse(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refusal = err
}

func (w *DNSWriter) Records() []edge.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.records)
}

func (w *DNSWriter) RecordTTL() time.Duration { return 60 * time.Second }

func (w *DNSWriter) ZoneOf(_ context.Context, hostname string) (edge.Zone, error) {
	if w.zone == "" || !edge.ZoneOwns(hostname, w.zone) {
		return edge.Zone{}, providerkit.Refuse(providerkit.CodeInvalid,
			"no zone reachable with these credentials owns %q", hostname)
	}
	return edge.Zone{ID: w.zone, Name: w.zone}, nil
}

func (w *DNSWriter) EnsureRecords(_ context.Context, records []edge.Record, say func(string)) ([]edge.Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return nil, w.refusal
	}
	for _, record := range records {
		if !slices.Contains(w.records, record) {
			w.records = append(w.records, record)
		}
		if say != nil {
			say("Wrote " + record.String())
		}
	}
	return slices.Clone(records), nil
}

func (w *DNSWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return w.refusal
	}
	w.records = slices.DeleteFunc(w.records, func(held edge.Record) bool {
		return slices.Contains(records, held)
	})
	return nil
}

var (
	_ providerkit.EdgeRegistry = (*Edges)(nil)
	_ providerkit.DNSRegistry  = (*DNS)(nil)
	_ edge.Edge                = (*Edge)(nil)
	_ edge.EdgeStack           = (*Stack)(nil)
	_ edge.DNSWriter           = (*DNSWriter)(nil)
	_ edge.TTLBound            = (*DNSWriter)(nil)
	_ edge.ZoneFinder          = (*DNSWriter)(nil)
	_ Ledger                   = (*ledger.Ledger)(nil)
)
