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

type EdgeOptions struct {
	Tunnel bool `json:"tunnel,omitempty"`
}

func (e *Edges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	e.mu.Lock()
	front, served := e.edges[kind]
	order := slices.Clone(e.order)
	e.mu.Unlock()
	if !served {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the reference provider serves no edge %q; it serves %s", kind, kindList(order))
	}
	decoded, err := provider.DecodeEdgeOptions[EdgeOptions](kind, options)
	if err != nil {
		return nil, err
	}
	return &Edge{edgeAccount: front.edgeAccount, options: decoded}, nil
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
		if !front.answers(hostname) {
			continue
		}
		forwarded, found := front.forwardedTo(hostname)
		if !found {
			return front.routedBy
		}
		for _, origin := range fronts {
			if Origin(origin.routedBy).Address == forwarded.Address {
				return origin.routedBy
			}
		}
		return front.routedBy
	}
	return ""
}

func (e *Edge) forwardedTo(hostname string) (edge.Origin, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	origin, found := e.forwards[hostname]
	return origin, found
}

func (e *Edges) Edge(kind edge.Kind) *Edge {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.edges[kind]
}

func (e *Edges) RouteOnlyThrough(kind edge.Kind, hostname string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for other, front := range e.edges {
		front.mu.Lock()
		if other == kind {
			if _, bound := front.serving[hostname]; !bound {
				front.serving[hostname] = ""
			}
		} else {
			delete(front.serving, hostname)
			delete(front.forwards, hostname)
		}
		front.mu.Unlock()
	}
}

func kindList(kinds []edge.Kind) string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return strings.Join(names, ", ")
}

type Edge struct {
	*edgeAccount
	options EdgeOptions
}

type edgeAccount struct {
	mu              sync.Mutex
	kind            edge.Kind
	routedBy        router.Kind
	owners          map[string]string
	wildcard        string
	specs           []edge.PreviewWildcardSpec
	stacks          []edge.StackSpec
	bindings        []edge.DomainBinding
	serving         map[string]string
	serves          *[]edge.Need
	forwards        map[string]edge.Origin
	addressesItself bool

	routerServes *[]edge.Need
	refusal      error
	unbound      error
	bindSays     string
	warns        string
	verify       func(context.Context) (edge.CredentialIdentity, error)

	unreadable  error
	entitlement *edge.CodeEntitlement

	proxies      bool
	issues       bool
	runsTunnels  bool
	tunnelEvents []string
	originLife   time.Duration
	issued       int
	held         map[string]string
	revoked      []string
	presenting   []string
	events       []string
	claims       []router.Claim
	entries      []router.Claim
	holding      []string
	released     []string
	disclaimed   []string
	purged       [][]string
	purgeError   error

	refusesCertified error
	refusesBinds     error
	onIssued         func()
	onEntryClaimed   func()
	permissions      *edge.CredentialDocument
}

func (e *Edge) RefusesClaimsCarryingAnOriginCertificate(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refusesCertified = err
}

func (e *Edge) RefusesBinds(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refusesBinds = err
}

func (e *Edge) OnOriginCertificateIssued(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onIssued = fn
}

func (e *Edge) OnPreviewEntryClaimed(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onEntryClaimed = fn
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

func ClientCA(kind edge.Kind) string {
	return "client certificate the " + string(kind) + " edge presents"
}

func Origin(kind router.Kind) edge.Origin {
	return edge.Origin{Address: "origin." + string(kind) + ".fake.invalid", Certified: true}
}

func (e *Edge) IssuesOriginCertificates() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.issues = true
}

func (e *Edge) IssuesOriginCertificatesExpiringIn(life time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.originLife = life
}

func (e *Edge) ForgetsOriginCertificate(hostname string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.held, hostname)
}

func (e *Edge) RevokedOriginCertificates() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.revoked)
}

func (e *Edge) issueOriginCertificate(_ context.Context, hostname string) (edge.OriginCertificate, error) {
	e.mu.Lock()
	e.issued++
	life := e.originLife
	if life == 0 {
		life = 365 * 24 * time.Hour
	}
	issued := edge.OriginCertificate{
		ID:          fmt.Sprintf("origin-certificate-%d", e.issued),
		Certificate: "origin certificate for " + hostname,
		Key:         "origin key for " + hostname,
		ExpiresAt:   time.Now().Add(life),
	}
	then := e.onIssued
	e.mu.Unlock()
	if then != nil {
		then()
	}
	return issued, nil
}

func (e *Edge) revokeOriginCertificate(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revoked = append(e.revoked, id)
	return nil
}

func (e *Edge) HoldsClientCAs(certificates ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.presenting = certificates
}

func (e *Edge) ClientCertificateEvents() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

func (e *Edge) ensureClientCertificates(context.Context, string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, "ensure")
	if e.presenting == nil {
		return []string{ClientCA(e.kind)}, nil
	}
	return slices.Clone(e.presenting), nil
}

func (e *Edge) presentClientCertificate(context.Context, string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, "present")
	return nil
}

func (e *Edge) recordClaim(claim router.Claim) (edge.Origin, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if claim.OriginCertificate.ID != "" && e.refusesCertified != nil {
		return edge.Origin{}, e.refusesCertified
	}
	e.events = append(e.events, "claim")
	e.claims = append(e.claims, claim)
	if !slices.Contains(e.holding, claim.Hostname) {
		e.holding = append(e.holding, claim.Hostname)
	}
	return e.holdOriginCertificate(claim), nil
}

func (e *Edge) recordPreviewEntryClaim(claim router.Claim) (edge.Origin, error) {
	e.mu.Lock()
	if claim.OriginCertificate.ID != "" && e.refusesCertified != nil {
		e.mu.Unlock()
		return edge.Origin{}, e.refusesCertified
	}
	e.events = append(e.events, "claim")
	e.entries = append(e.entries, claim)
	origin := e.holdOriginCertificate(claim)
	then := e.onEntryClaimed
	e.mu.Unlock()
	if then != nil {
		then()
	}
	return origin, nil
}

func (e *Edge) recordPreviewEntryDisclaim(baseDomain string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.released = append(e.released, baseDomain)
}

func (e *Edge) PreviewEntryClaims() []router.Claim {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.entries)
}

func (e *Edge) DisclaimedPreviewEntries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.released)
}

func (e *Edge) holdOriginCertificate(claim router.Claim) edge.Origin {
	if claim.Tunnel != edge.None {
		return edge.Origin{Address: FormatTunnelAddress(claim.Tunnel), Certified: true, Tunneled: true}
	}
	if claim.OriginCertificate.ID != "" {
		if e.held == nil {
			e.held = map[string]string{}
		}
		e.held[claim.Hostname] = claim.OriginCertificate.ID
	}
	origin := Origin(e.routedBy)
	origin.Certified = !e.issues || e.held[claim.Hostname] != ""
	return origin
}

func (e *Edge) recordDisclaim(hostname string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disclaimed = append(e.disclaimed, hostname)
	e.holding = slices.DeleteFunc(e.holding, func(held string) bool { return held == hostname })
	delete(e.held, hostname)
}

func (e *Edge) listHeldClaims() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.holding)
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

func (e *Edge) bound(binding edge.DomainBinding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusesBinds != nil {
		return e.refusesBinds
	}
	e.bindings = append(e.bindings, binding)
	e.serving[binding.Hostname] = binding.Certificate
	if binding.Origin == nil {
		delete(e.forwards, binding.Hostname)
	} else {
		if e.forwards == nil {
			e.forwards = map[string]edge.Origin{}
		}
		e.forwards[binding.Hostname] = *binding.Origin
	}
	if e.bindSays != "" && binding.Say != nil {
		binding.Say(e.bindSays)
	}
	return nil
}

func (e *Edge) release(hostname string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.serving, hostname)
	delete(e.forwards, hostname)
	return progress.MarkWarning(e.unbound)
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
	return &Edge{edgeAccount: &edgeAccount{kind: kind, routedBy: routedBy, owners: map[string]string{}, serving: map[string]string{}}}
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
		TunnelsToOrigin: e.options.Tunnel,
	}
	if e.proxies {
		facts.ProxiedRecordNote = "Turn the fake edge's proxy on for these records."
	}
	if e.serves != nil {
		facts.Supported = slices.Clone(*e.serves)
	}
	if e.kind == KindRelay {
		facts.Compatibility = edge.Compatibility{Date: CompatDate, Flags: []string{CompatFlag}}
		facts.Entry = edge.WorkerModule{
			Name:        "index.js",
			ContentType: "application/javascript+module",
			Content:     []byte("the fake entry worker"),
		}
	}
	return facts
}

func (e *Edge) AddressesItself(addresses bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addressesItself = addresses
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
		hooks.ClientCertificates = &edge.ClientCertificateHooks{Ensure: e.ensureClientCertificates, Present: e.presentClientCertificate}
		hooks.PurgeHostnames = e.purge
	}
	if e.runsTunnels {
		hooks.Tunnels = &edge.TunnelHooks{Ensure: e.ensureTunnel, Configure: e.configureTunnel, ReadToken: e.readTunnelToken, Delete: e.deleteTunnel}
	}
	if e.issues {
		hooks.OriginCertificates = &edge.OriginCertificateHooks{Issue: e.issueOriginCertificate, Revoke: e.revokeOriginCertificate}
	}
	if e.entitlement != nil {
		granted := *e.entitlement
		hooks.CheckCodeEntitlement = func(context.Context) (edge.CodeEntitlement, error) { return granted, nil }
	}
	if e.permissions != nil {
		documented := *e.permissions
		hooks.DescribeCredentialPermissions = func(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
			return edge.CredentialDocument{Heading: documented.Heading, Document: documented.Document + " for " + string(purpose)}, nil
		}
	}
	return hooks
}

func (e *Edge) DocumentsPermissions(document edge.CredentialDocument) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissions = &document
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

func (e *Edge) RouterServes(needs []edge.Need) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.routerServes = &needs
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
	state.Address = e.front(spec.Slug)
	return e.open(state)
}

func (e *Edge) Open(state edge.StackState) (edge.EdgeStack, error) { return e.open(state) }

func (e *Edge) open(state edge.StackState) (*Stack, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusal != nil {
		return nil, e.refusal
	}
	if state.Address == "" {
		state.Address = e.front(state.Slug)
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
	if err := s.front.bound(binding); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Bind(binding.Hostname)
	if binding.Origin == nil {
		s.state.PublishAddress(binding.Hostname, s.front.front(s.state.Slug))
		return nil
	}
	s.state.PublishAddress(binding.Hostname, binding.Origin.Address)
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
	s.state.PublishAddress(hostname, "")
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

func FormatTunnelAddress(kind edge.Kind) string { return "tunnel." + string(kind) + ".fake.invalid" }

func (e *Edge) RunsTunnels() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runsTunnels = true
}

func (e *Edge) TunnelEvents() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.tunnelEvents)
}

func (e *Edge) recordTunnelEvent(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tunnelEvents = append(e.tunnelEvents, event)
}

func (e *Edge) ensureTunnel(_ context.Context, name string) (edge.Tunnel, error) {
	e.recordTunnelEvent("ensure " + name)
	return edge.Tunnel{ID: "tunnel-" + name, Address: FormatTunnelAddress(e.kind)}, nil
}

func (e *Edge) configureTunnel(_ context.Context, id, service string) error {
	e.recordTunnelEvent("configure " + id + " " + service)
	return nil
}

func (e *Edge) readTunnelToken(_ context.Context, id string) (string, error) {
	e.recordTunnelEvent("token " + id)
	return "token-" + id, nil
}

func (e *Edge) deleteTunnel(_ context.Context, id string) error {
	e.recordTunnelEvent("delete " + id)
	return nil
}
