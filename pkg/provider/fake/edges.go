package fake

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	KindRelay  edge.Kind = "relay"
	KindDirect edge.Kind = "direct"
)

const (
	RouterRelay  router.Kind = "relay-router"
	RouterDirect router.Kind = "direct-router"
)

type Edges struct {
	mu    sync.Mutex
	order []edge.Kind
	edges map[edge.Kind]*Edge
}

func NewEdges() *Edges {
	registry := &Edges{edges: map[edge.Kind]*Edge{}}
	for kind, routedBy := range map[edge.Kind]router.Kind{KindRelay: RouterRelay, KindDirect: RouterDirect} {
		registry.edges[kind] = newEdge(kind, routedBy)
	}
	registry.order = []edge.Kind{KindRelay, KindDirect}
	return registry
}

func (e *Edges) kinds() []edge.Kind {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.order)
}

func (e *Edges) Open(kind edge.Kind) (edge.Edge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	front, served := e.edges[kind]
	if !served {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the reference provider serves no edge %q; it serves %s", kind, kindList(e.order))
	}
	return front, nil
}

func (e *Edges) Verifies(kind edge.Kind, identity edge.CredentialIdentity, err error) {
	front := e.Edge(kind)
	front.mu.Lock()
	defer front.mu.Unlock()
	front.verify = func(context.Context) (edge.CredentialIdentity, error) { return identity, err }
}

func (e *Edges) serving(certificate string) bool {
	e.mu.Lock()
	fronts := slices.Collect(maps.Values(e.edges))
	e.mu.Unlock()
	for _, front := range fronts {
		if front.Serving(certificate) {
			return true
		}
	}
	return false
}

func (e *Edges) answering(hostname string) router.Kind {
	e.mu.Lock()
	fronts := slices.Collect(maps.Values(e.edges))
	e.mu.Unlock()
	for _, front := range fronts {
		if front.answers(hostname) {
			return front.routedBy
		}
	}
	return ""
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
	routedBy router.Kind
	owners   map[string]string
	wildcard string
	specs    []edge.PreviewWildcardSpec
	stacks   []edge.StackSpec
	bindings []edge.DomainBinding
	serving  map[string]string
	serves   *[]edge.Need
	byLabel  bool
	refusal  error
	unbound  error
	bindSays string
	warns    string
	verify   func(context.Context) (edge.CredentialIdentity, error)

	unreadable  error
	entitlement *edge.CodeEntitlement

	proxies    bool
	claims     []router.Claim
	disclaimed []string
	purged     [][]string
	purgeError error
}

func (e *Edge) RefusePurges(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.purgeError = err
}

func (e *Edge) Purged() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.purged)
}

func (e *Edge) purge(_ context.Context, hostnames []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.purgeError != nil {
		return e.purgeError
	}
	e.purged = append(e.purged, slices.Clone(hostnames))
	return nil
}

func (e *Edge) ProxiesRecords() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.proxies = true
}

func ClientCertificate(kind edge.Kind) string {
	return "client certificate the " + string(kind) + " edge presents"
}

func Origin(kind router.Kind) edge.Origin {
	return edge.Origin{Address: "origin." + string(kind) + ".fake.invalid"}
}

func (e *Edge) recordClaim(claim router.Claim) edge.Origin {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.claims = append(e.claims, claim)
	return Origin(e.routedBy)
}

func (e *Edge) recordDisclaim(hostname string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disclaimed = append(e.disclaimed, hostname)
}

func (e *Edge) heldClaims() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var held []string
	for _, claim := range e.claims {
		if !slices.Contains(held, claim.Hostname) {
			held = append(held, claim.Hostname)
		}
	}
	return slices.DeleteFunc(held, func(hostname string) bool { return slices.Contains(e.disclaimed, hostname) })
}

func (e *Edge) Claims() []router.Claim {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.claims)
}

func (e *Edge) Disclaimed() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.disclaimed)
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
	e.serving[binding.Hostname] = binding.Certificate
	if e.bindSays != "" && binding.Say != nil {
		binding.Say(e.bindSays)
	}
}

func (e *Edge) release(hostname string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.serving, hostname)
	return progress.Warned(e.unbound)
}

func (e *Edge) answers(hostname string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, bound := e.serving[hostname]; bound {
		return true
	}
	return e.wildcard != "" && hostname == edge.PreviewWildcard(e.wildcard)
}

func (e *Edge) Serving(certificate string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return certificate != "" && slices.Contains(slices.Collect(maps.Values(e.serving)), certificate)
}

func newEdge(kind edge.Kind, routedBy router.Kind) *Edge {
	return &Edge{kind: kind, routedBy: routedBy, owners: map[string]string{}, serving: map[string]string{}}
}

func (e *Edge) SayOnBind(said string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bindSays = said
}

func (e *Edge) WarnOnReconcile(warning string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.warns = warning
}

func (e *Edge) warn(warn func(string)) {
	e.mu.Lock()
	warning := e.warns
	e.mu.Unlock()
	if warning != "" && warn != nil {
		warn(warning)
	}
}

func (e *Edge) WarnOnUnbind(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unbound = err
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
	e.mu.Lock()
	defer e.mu.Unlock()
	facts := edge.Facts{
		Supported:       edge.AllNeeds(),
		RunsCode:        e.kind == KindRelay,
		ProxiesRecords:  e.proxies,
		CredentialScope: "fake-account",
	}
	if e.serves != nil {
		facts.Supported = slices.Clone(*e.serves)
	}
	if e.kind == KindRelay {
		facts.Compatibility = edge.Compatibility{Date: CompatDate, Flags: []string{CompatFlag}}
	}
	return facts
}

func (e *Edge) RoutesPreviewsByLabel(routes bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byLabel = routes
}

const (
	CompatDate = "2025-01-01"
	CompatFlag = "nodejs_compat"
)

func (e *Edge) Hooks() edge.Hooks {
	e.mu.Lock()
	defer e.mu.Unlock()
	hooks := edge.Hooks{VerifyCredentials: e.verify}
	if e.proxies {
		hooks.EnsureClientCertificate = func(context.Context, string) (string, error) { return ClientCertificate(e.kind), nil }
		hooks.PurgeHostnames = e.purge
	}
	if e.entitlement != nil {
		granted := *e.entitlement
		hooks.CheckCodeEntitlement = func(context.Context) (edge.CodeEntitlement, error) { return granted, nil }
	}
	return hooks
}

func (e *Edge) Entitles(granted edge.CodeEntitlement) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entitlement = &granted
}

func (e *Edge) Serves(needs []edge.Need) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.serves = &needs
}

func (e *Edge) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustInternal}, nil
}

func (e *Edge) Teardown(context.Context, environment.Tier) error { return nil }

func (e *Edge) Stacks() []edge.StackSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.stacks)
}

func (e *Edge) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	e.mu.Lock()
	e.stacks = append(e.stacks, spec)
	e.mu.Unlock()
	e.warn(spec.Warn)
	state := prior
	state.Slug, state.Tier = spec.Slug, spec.Tier
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
	return &Stack{front: e, state: state}, nil
}

func (e *Edge) front(slug string) string {
	return slug + "." + string(e.kind) + ".fake.invalid"
}

func (e *Edge) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	e.warn(spec.Warn)
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
	if e.unreadable != nil {
		return "", e.unreadable
	}
	return e.owners[hostname], nil
}

func (e *Edge) OwnersUnreadable(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unreadable = err
}

func (e *Edge) ProjectOwner(slug string, tier environment.Tier) string {
	return "ocel-" + slug + "-" + string(tier)
}

func (e *Edge) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	changes := []edge.PlanChange{{
		Kind:   "Fake::EdgeStack",
		Name:   scope.Slug + "-" + string(scope.Tier),
		Action: edge.PlanDelete,
		Reason: "the " + string(e.kind) + " edge stack this project deploys through",
	}}
	for _, hostname := range scope.Hostnames {
		changes = append(changes, edge.PlanChange{
			Kind:   "Fake::Hostname",
			Name:   hostname,
			Action: edge.PlanDelete,
		})
	}
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(e.kind),
		Action:  edge.PlanDelete,
		Changes: changes,
	}}
}

func (e *Edge) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(e.kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind:   "Fake::PreviewEntry",
			Name:   wildcard,
			Action: edge.PlanDelete,
			Reason: "the shared entry every preview on this wildcard is served through",
		}},
	}, e.SharedPreviewRemoval()
}

func (e *Edge) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(e.kind),
		Action: edge.PlanKeep,
		Reason: "shared with every other wildcard in this account: " + edge.PreviewEntryOwner,
	}
}

type Stack struct {
	front *Edge
	mu    sync.Mutex
	state edge.StackState
}

func (s *Stack) State() edge.StackState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Stack) BindDomain(_ context.Context, binding edge.DomainBinding) error {
	s.front.bound(binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Bind(binding.Hostname)
	if binding.Origin == nil {
		s.state.PublishFront(binding.Hostname, s.front.front(s.state.Slug))
		return nil
	}
	s.state.PublishFront(binding.Hostname, binding.Origin.Address)
	written, err := edge.RecordsFor(edge.TargetOf(s.front.kind, s.front.Facts(), s.state), []string{binding.Hostname})
	if err != nil {
		return err
	}
	s.state.RecordWrites(append(slices.Clone(s.state.Records), written...))
	return nil
}

func (s *Stack) UnbindDomain(_ context.Context, hostname string) error {
	warned := s.front.release(hostname)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return warned
}

func (s *Stack) Destroy(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = edge.StackState{}
	return nil
}

var (
	_ provider.Edges  = (*Edges)(nil)
	_ provider.DNS    = (*DNS)(nil)
	_ edge.Edge       = (*Edge)(nil)
	_ edge.EdgeStack  = (*Stack)(nil)
	_ edge.DNSRecords = (*DNSRecords)(nil)
)
