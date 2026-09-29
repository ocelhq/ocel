package edgeconformance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"maps"
	"net/netip"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type Suite struct {
	New       func(t *testing.T) (edge.Edge, edge.StackSpec)
	Hostname  string
	Origin    *edge.Origin
	Previews  func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec)
	Bootstrap func(t *testing.T) (edge.Edge, environment.Tier)
}

func Run(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Hostname == "" {
		t.Fatal("the suite names no hostname, and every edge must be able to bind one")
	}

	t.Run("the code fact and the compatibility fact are one answer", func(t *testing.T) {
		e, _ := suite.New(t)
		runsCode := e.Facts().RunsCode
		if names := !e.Facts().Compatibility.IsZero(); runsCode != names {
			t.Errorf("Facts().RunsCode = %v, but Facts().Compatibility named = %v; the code an edge runs loads under the compatibility it names, so an edge cannot answer them differently", runsCode, names)
		}
		wants := slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool {
			return edge.Supports(e, need)
		})
		if runsCode != wants {
			t.Errorf("Facts().RunsCode = %v, but Facts().Supported names a code need = %v; an edge that runs code must declare one and one that does not must declare neither", runsCode, wants)
		}
	})

	t.Run("every declared need is a need, and declared once", func(t *testing.T) {
		e, _ := suite.New(t)
		supported := e.Facts().Supported
		for i, need := range supported {
			if !edge.ValidNeed(need) {
				t.Errorf("Facts().Supported names %q, which is not a need", need)
			}
			if slices.Contains(supported[:i], need) {
				t.Errorf("Facts().Supported names %q twice", need)
			}
		}
	})

	t.Run("teardown plans are named, typed and honestly actioned", func(t *testing.T) {
		e, spec := suite.New(t)

		groups := e.ProjectRemovals(edge.ProjectScope{
			Slug:      spec.Slug,
			Tier:      spec.Tier,
			Hostnames: []string{suite.Hostname},
			Address:   "front.example.net",
		})
		if len(groups) == 0 {
			t.Fatal("ProjectRemovals = none, want what a project with a bound hostname depends on")
		}
		for _, group := range groups {
			checkRemoval(t, "ProjectRemovals", group)
		}

		removed, kept := e.PreviewWildcardRemovals("*.preview.example.com")
		checkRemoval(t, "PreviewWildcardRemovals removed", removed)
		checkRemoval(t, "PreviewWildcardRemovals kept", kept)
		if removed.Action == edge.PlanKeep {
			t.Error("PreviewWildcardRemovals' removed group is kept; releasing the wildcard must take down what serves it")
		}
		if len(removed.Changes) == 0 {
			t.Error("PreviewWildcardRemovals' removed group has no rows; a removal plan names what goes")
		}
		if kept.Action != edge.PlanKeep {
			t.Errorf("PreviewWildcardRemovals' kept group has action %q, want %q", kept.Action, edge.PlanKeep)
		}

		shared := e.SharedPreviewRemoval()
		checkRemoval(t, "SharedPreviewRemoval", shared)
		if shared.Action != edge.PlanKeep {
			t.Errorf("SharedPreviewRemoval action = %q, want %q: it is bootstrap-scoped", shared.Action, edge.PlanKeep)
		}
	})

	t.Run("binding a domain twice binds it once and shows in state", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		suite.binds(t, stack)
		if err := stack.BindDomain(ctx, suite.binding()); err != nil {
			t.Fatalf("BindDomain again: %v", err)
		}

		bound := stack.State().Bound
		if len(bound) != 1 || !slices.Contains(bound, suite.Hostname) {
			t.Errorf("bound domains = %v, want exactly %q", bound, suite.Hostname)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner == "" {
			t.Errorf("DomainOwner(%q) = %q, want the surface the binding created", suite.Hostname, owner)
		}
	})

	t.Run("the surface a project's own binding creates is the one the project can name", func(t *testing.T) {
		ctx := context.Background()
		e, spec := suite.New(t)
		stack, err := e.Reconcile(ctx, spec, edge.StackState{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		suite.binds(t, stack)

		mine := e.ProjectOwner(spec.Slug, spec.Tier)
		if mine == "" {
			t.Skip("this edge names no project-wide surface, so a hostname it serves is owned by something finer than the project")
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != mine {
			t.Errorf("DomainOwner(%q) = %q after this project bound it, but ProjectOwner(%q, %q) = %q. The preflight guard refuses a hostname whose owner is not the name this call mints, so two spellings of one surface refuse a project its own hostname whenever a bind wrote the route and stopped before the record",
				suite.Hostname, owner, spec.Slug, spec.Tier, mine)
		}
	})

	t.Run("a bound domain has a front to point DNS at", func(t *testing.T) {
		e, stack := reconciledOn(t, suite)
		suite.binds(t, stack)

		records := frontedRecords(t, e, stack.State(), suite.Hostname)

		reopened, err := e.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		reread := frontedRecords(t, e, reopened.State(), suite.Hostname)
		if !slices.Equal(reread, records) {
			t.Errorf("records through a reopened stack = %v, want the %v the binding published", reread, records)
		}
	})

	t.Run("the binding publishes the front, not some reconcile before it", func(t *testing.T) {
		e, reconciled := reconciledOn(t, suite)
		stack, err := e.Open(withoutFronts(reconciled.State()))
		if err != nil {
			t.Fatalf("Open a state with no front: %v", err)
		}
		suite.binds(t, stack)

		frontedRecords(t, e, stack.State(), suite.Hostname)
	})

	t.Run("a binding reports the state change the origin persists on", func(t *testing.T) {
		_, stack := reconciledOn(t, suite)

		before := stack.State()
		suite.binds(t, stack)
		if after := stack.State(); after.Equal(before) {
			t.Errorf("state = %+v both before and after %q was bound; the origin writes what a call reports as changed, so a binding that reports nothing is lost the moment the process ends", after, suite.Hostname)
		}
	})

	t.Run("state survives the seam it is persisted through", func(t *testing.T) {
		e, stack := reconciledOn(t, suite)
		suite.binds(t, stack)

		written := stack.State()
		persisted := roundTrip(t, written)
		if !persisted.Equal(written) {
			t.Errorf("state read back = %+v, want the %+v it was written from; everything a stack keeps travels through this one encoding, including what the edge keeps to itself", persisted, written)
		}

		reopened, err := e.Open(persisted)
		if err != nil {
			t.Fatalf("Open a persisted state: %v", err)
		}
		frontedRecords(t, e, reopened.State(), suite.Hostname)
	})

	t.Run("an edge that proxies records and runs no code forwards a hostname bound with an origin to that origin", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		if facts := e.Facts(); !facts.ProxiesRecords || facts.RunsCode {
			t.Skip("this edge answers every hostname it binds itself")
		}
		origin := edge.Origin{Address: "203.0.113.7"}
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: suite.Hostname, Origin: &origin}); err != nil {
			t.Fatalf("BindDomain with origin %+v: %v", origin, err)
		}
		t.Cleanup(func() {
			if err := stack.UnbindDomain(context.Background(), suite.Hostname); err != nil {
				t.Errorf("UnbindDomain(%q) releasing what this obligation bound: %v", suite.Hostname, err)
			}
		})

		records, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), []string{suite.Hostname})
		if err != nil {
			t.Fatalf("RecordsFor(%q): %v", suite.Hostname, err)
		}
		want := edge.Record{Name: suite.Hostname, Type: edge.RecordTypeA, Value: origin.Address, Proxied: true}
		if len(records) != 1 || records[0] != want {
			t.Errorf("records = %v, want the one %v: a hostname bound with an origin is forwarded there through the edge's proxy", records, want)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner == "" {
			t.Errorf("DomainOwner(%q) = %q after it was bound with an origin, want the surface the binding created", suite.Hostname, owner)
		}
	})

	t.Run("an edge presents only a client certificate its origins were told to trust", func(t *testing.T) {
		e, _ := suite.New(t)
		certificates := e.Hooks().ClientCertificates
		if certificates == nil {
			t.Skip("this edge presents no client certificate to its origins")
		}
		ctx := context.Background()
		trusted, err := certificates.Ensure(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("Ensure(%q): %v", suite.Hostname, err)
		}
		if len(trusted) == 0 {
			t.Fatalf("Ensure(%q) names no certificate, and an origin that trusts none refuses every request the edge forwards", suite.Hostname)
		}
		for _, certificate := range trusted {
			requireClientCertificate(t, certificate)
		}
		if err := certificates.Present(ctx, suite.Hostname); err != nil {
			t.Fatalf("Present(%q): %v", suite.Hostname, err)
		}
		presented, err := certificates.Ensure(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("Ensure(%q) once presented: %v", suite.Hostname, err)
		}
		if !slices.Equal(presented, trusted) {
			t.Errorf("Ensure(%q) = %d certificates once presented, %d before: an edge that holds a certificate mints no other, and an origin claimed with what it named first refuses anything new", suite.Hostname, len(presented), len(trusted))
		}
	})

	t.Run("an origin certificate an edge issues covers the hostname and pairs with its key", func(t *testing.T) {
		e, _ := suite.New(t)
		certificates := e.Hooks().OriginCertificates
		if certificates == nil {
			t.Skip("this edge issues no origin certificate")
		}
		ctx := context.Background()
		issued, err := certificates.Issue(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("Issue(%q): %v", suite.Hostname, err)
		}
		if issued.ID == "" {
			t.Errorf("Issue(%q) names no id, so nothing can revoke it once it is replaced", suite.Hostname)
		}
		pair, err := tls.X509KeyPair([]byte(issued.Certificate), []byte(issued.Key))
		if err != nil {
			t.Fatalf("Issue(%q) handed a certificate and key that do not pair: %v", suite.Hostname, err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatalf("parse the origin certificate: %v", err)
		}
		if err := leaf.VerifyHostname(suite.Hostname); err != nil {
			t.Errorf("the origin certificate does not cover %s: %v", suite.Hostname, err)
		}
		if err := certificates.Revoke(ctx, issued.ID); err != nil {
			t.Errorf("Revoke(%q): %v", issued.ID, err)
		}
	})

	t.Run("unbinding a domain twice leaves nothing bound", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)

		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain before any binding: %v", err)
		}
		if err := stack.BindDomain(ctx, suite.binding()); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain: %v", err)
		}
		if err := stack.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain again: %v", err)
		}

		if bound := stack.State().Bound; len(bound) != 0 {
			t.Errorf("bound domains = %v, want none once the host is unbound", bound)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want nothing serving an unbound host", suite.Hostname, owner)
		}
	})

	t.Run("a hostname one surface releases is one the next can bind", func(t *testing.T) {
		ctx := context.Background()
		_, first := reconciledOn(t, suite)
		if err := first.BindDomain(ctx, suite.binding()); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if err := first.UnbindDomain(ctx, suite.Hostname); err != nil {
			t.Fatalf("UnbindDomain: %v", err)
		}

		e, second := reconciledOn(t, suite)
		suite.binds(t, second)

		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner == "" {
			t.Errorf("DomainOwner(%q) = %q after the surface that owned it released it and another bound it; a name the first project gives up has to come back into circulation, or moving a domain between projects needs an edge no one can reach", suite.Hostname, owner)
		}
	})

	runPreviews(t, suite)

	runBootstrap(t, suite)

	t.Run("destroying a stack takes the domains it bound with it", func(t *testing.T) {
		ctx := context.Background()
		e, stack := reconciledOn(t, suite)
		if err := stack.BindDomain(ctx, suite.binding()); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}

		if err := stack.Destroy(ctx); err != nil {
			t.Fatalf("Destroy: %v", err)
		}

		if bound := stack.State().Bound; len(bound) != 0 {
			t.Errorf("bound domains = %v, want none after the stack was destroyed", bound)
		}
		owner, err := e.DomainOwner(ctx, suite.Hostname)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want no surface left after Destroy", suite.Hostname, owner)
		}
	})
}

func checkRemoval(t *testing.T, what string, group edge.PlanGroup) {
	t.Helper()

	if group.Kind != edge.EdgeGroupKind || group.Name == "" {
		t.Errorf("%s: group %+v must be an edge group with a name", what, group)
	}
	if !edge.ValidPlanAction(group.Action) {
		t.Errorf("%s: group %+v names no valid action", what, group)
	}
	if group.Action == edge.PlanKeep && group.Reason == "" {
		t.Errorf("%s: group %+v is kept and says nothing about why", what, group)
	}
	for _, change := range group.Changes {
		if change.Kind == "" || change.Name == "" {
			t.Errorf("%s: row %+v must have a resource type and a name", what, change)
		}
		if !edge.ValidPlanAction(change.Action) {
			t.Errorf("%s: row %+v names no valid action", what, change)
		}
	}
}

func frontedRecords(t *testing.T, e edge.Edge, state edge.StackState, hostname string) []edge.Record {
	t.Helper()

	bound := state.Bound
	if !slices.Contains(bound, hostname) {
		t.Fatalf("bound domains = %v, want %q among them", bound, hostname)
	}
	target := edge.TargetFor(e, state)
	front := target.AddressFor(hostname)
	if target.ServesUnbound {
		if front != "" {
			t.Errorf("the front for %q is %q, but a %s edge answers on the zone itself and publishes none", hostname, front, e.Kind())
		}
	} else if front == "" {
		t.Fatalf("state = %v, want a front for %q on it: a %s edge answers on a hostname of its own, and DNS has nothing to point at until the state contains it", state, hostname, e.Kind())
	}

	records, err := edge.RecordsFor(target, bound)
	if err != nil {
		t.Fatalf("RecordsFor(%v): %v", bound, err)
	}
	if len(records) != len(bound) {
		t.Fatalf("records = %v, want one per bound hostname %v", records, bound)
	}
	at := slices.IndexFunc(records, func(rec edge.Record) bool { return rec.Name == hostname })
	if at < 0 {
		t.Fatalf("records = %v, want one of them at %q", records, hostname)
	}
	rec := records[at]
	if rec.Proxied != target.ProxiesRecords {
		t.Errorf("record %v is proxied = %t, want %t: a record is proxied exactly when Facts().ProxiesRecords says the edge proxies what it points at", rec, rec.Proxied, target.ProxiesRecords)
	}
	addr, addrErr := netip.ParseAddr(rec.Value)
	switch rec.Type {
	case edge.RecordTypeA:
		if addrErr != nil || !addr.Unmap().Is4() {
			t.Errorf("record %v is an A record, and %q is no IPv4 address a resolver would accept in one", rec, rec.Value)
		}
	case edge.RecordTypeAAAA:
		if addrErr != nil || addr.Unmap().Is4() {
			t.Errorf("record %v is an AAAA record, and %q is no IPv6 address a resolver would accept in one", rec, rec.Value)
		}
	}
	if target.ServesUnbound {
		if rec.Value != edge.ProxyPlaceholder {
			t.Errorf("record %v points at %q, want the %q placeholder a proxied record points at", rec, rec.Value, edge.ProxyPlaceholder)
		}
		return records
	}
	if !pointsAt(rec.Value, front) {
		t.Errorf("record %v points at %q, want the %q the %s edge published", rec, rec.Value, front, e.Kind())
	}
	return records
}

func pointsAt(value, front string) bool {
	got, gotErr := netip.ParseAddr(value)
	want, wantErr := netip.ParseAddr(front)
	if gotErr == nil && wantErr == nil {
		return got.Unmap() == want.Unmap()
	}
	return value == front
}

func roundTrip(t *testing.T, state edge.StackState) edge.StackState {
	t.Helper()

	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal %+v: %v", state, err)
	}
	var read edge.StackState
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return read
}

func withoutFronts(state edge.StackState) edge.StackState {
	state.Address, state.Addresses = "", nil
	return state
}

func (s Suite) binding() edge.DomainBinding {
	return edge.DomainBinding{Hostname: s.Hostname, Origin: s.Origin}
}

func (s Suite) binds(t *testing.T, stack edge.EdgeStack) {
	t.Helper()

	hostname := s.Hostname
	if err := stack.BindDomain(context.Background(), s.binding()); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	t.Cleanup(func() {
		if err := stack.UnbindDomain(context.Background(), hostname); err != nil {
			t.Errorf("UnbindDomain(%q) releasing what this obligation bound: %v", hostname, err)
		}
	})
}

func reconciledOn(t *testing.T, suite Suite) (edge.Edge, edge.EdgeStack) {
	t.Helper()
	e, spec := suite.New(t)
	stack, err := e.Reconcile(context.Background(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return e, stack
}

func offerKinds(offers []edge.Offer) []edge.OfferKind {
	kinds := make([]edge.OfferKind, 0, len(offers))
	for _, offer := range offers {
		kinds = append(kinds, offer.Kind)
	}
	slices.Sort(kinds)
	return kinds
}

func checkOffers(t *testing.T, what string, out edge.BootstrapOutput) {
	t.Helper()

	if out.Trust != edge.TrustExternal && out.Trust != edge.TrustInternal {
		t.Errorf("%s: Trust = %q, want external or internal", what, out.Trust)
	}
	for i, offer := range out.Offers {
		if offer.Kind == "" {
			t.Errorf("%s: offer %d names no kind", what, i)
		}
		if len(offer.Values) == 0 {
			t.Errorf("%s: offer %q has no values; the origin has nothing to adopt", what, offer.Kind)
		}
		if slices.ContainsFunc(out.Offers[:i], func(prior edge.Offer) bool { return prior.Kind == offer.Kind }) {
			t.Errorf("%s: offer %q is made twice", what, offer.Kind)
		}
	}
}

func runBootstrap(t *testing.T, suite Suite) {
	t.Run("bootstrap and teardown round trip", func(t *testing.T) {
		if suite.Bootstrap == nil {
			t.Skip("this edge cannot be bootstrapped from the conformance suite alone")
		}
		ctx := context.Background()
		e, tier := suite.Bootstrap(t)

		first, err := e.Bootstrap(ctx, tier)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		checkOffers(t, "first Bootstrap", first)

		second, err := e.Bootstrap(ctx, tier)
		if err != nil {
			t.Fatalf("Bootstrap again: %v", err)
		}
		checkOffers(t, "second Bootstrap", second)
		if !slices.Equal(offerKinds(second.Offers), offerKinds(first.Offers)) {
			t.Errorf("second Bootstrap offered %v, want the %v the first did: a re-run converges on what is already installed", offerKinds(second.Offers), offerKinds(first.Offers))
		}
		if !maps.Equal(second.Values, first.Values) {
			t.Errorf("second Bootstrap values = %v, want the %v the first published", second.Values, first.Values)
		}

		if adopt := e.Hooks().PlanAdoption; adopt != nil {
			adoption, err := adopt(ctx, tier)
			if err != nil {
				t.Fatalf("PlanAdoption: %v", err)
			}
			if !maps.Equal(adoption.Values, first.Values) {
				t.Errorf("Adoption values = %v, want the %v Bootstrap published; an origin adopting an installed edge must land on the same coordinates", adoption.Values, first.Values)
			}
			offered := slices.Clone(adoption.Offers)
			slices.Sort(offered)
			if !slices.Equal(offered, offerKinds(first.Offers)) {
				t.Errorf("Adoption offers %v, want the %v Bootstrap made", offered, offerKinds(first.Offers))
			}
		}

		if err := e.Teardown(ctx, tier); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if err := e.Teardown(ctx, tier); err != nil {
			t.Fatalf("Teardown again: %v", err)
		}

		again, err := e.Bootstrap(ctx, tier)
		if err != nil {
			t.Fatalf("Bootstrap after Teardown: %v", err)
		}
		checkOffers(t, "Bootstrap after Teardown", again)
		if !slices.Equal(offerKinds(again.Offers), offerKinds(first.Offers)) {
			t.Errorf("Bootstrap after Teardown offered %v, want the %v a fresh account gets", offerKinds(again.Offers), offerKinds(first.Offers))
		}
	})
}

func runPreviews(t *testing.T, suite Suite) {
	t.Run("previews served on a shared wildcard", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this edge cannot raise a preview wildcard from the conformance suite alone")
		}

		t.Run("reconciling the wildcard twice publishes the same front", func(t *testing.T) {
			ctx := context.Background()
			e, _, wildcard := suite.Previews(t)

			first, err := e.ReconcilePreviewWildcard(ctx, wildcard)
			if err != nil {
				t.Fatalf("ReconcilePreviewWildcard: %v", err)
			}
			second, err := e.ReconcilePreviewWildcard(ctx, wildcard)
			if err != nil {
				t.Fatalf("ReconcilePreviewWildcard again: %v", err)
			}
			if second != first {
				t.Errorf("front = %q on the second reconcile, want the %q the first published; a resumed `ocel domain use` must not move where DNS points", second, first)
			}
		})

		t.Run("destroying the wildcard after reconciling it is clean and re-entrant", func(t *testing.T) {
			ctx := context.Background()
			e, _, wildcard := suite.Previews(t)
			if _, err := e.ReconcilePreviewWildcard(ctx, wildcard); err != nil {
				t.Fatalf("ReconcilePreviewWildcard: %v", err)
			}
			if err := e.DestroyPreviewWildcard(ctx, wildcard.BaseDomain); err != nil {
				t.Fatalf("DestroyPreviewWildcard: %v", err)
			}
			if err := e.DestroyPreviewWildcard(ctx, wildcard.BaseDomain); err != nil {
				t.Fatalf("DestroyPreviewWildcard again: %v", err)
			}
		})
	})
}

func requireClientCertificate(t *testing.T, certificate string) {
	t.Helper()
	block, _ := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("staged %q, want a PEM certificate", certificate)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the client certificate: %v", err)
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Errorf("the client certificate is good for %v, want client authentication among them", leaf.ExtKeyUsage)
	}
}
