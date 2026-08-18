package cloudflare

import (
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const domainEntryScript = "ocel-acme-web-prod"

func domainStack(t *testing.T, m *cfMock) *stack {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	return stackOn(m.provider(t), edge.StackState{
		edge.StackKeySlug:   "acme-web",
		stackKeyEntryWorker: domainEntryScript,
	})
}

func zoneMock() *cfMock {
	return &cfMock{zoneID: "zone1", zoneName: "app.com"}
}

func TestBindDomain(t *testing.T) {
	t.Run("a host Universal SSL covers gets a route and nothing in the zone's records", func(t *testing.T) {
		m := zoneMock()
		s := domainStack(t, m)

		if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}

		if len(m.createdRoutes) != 1 {
			t.Fatalf("created routes = %v, want the bound host's route", m.createdRoutes)
		}
		if m.createdRoutes[0]["pattern"] != "shop.app.com/*" || m.createdRoutes[0]["script"] != domainEntryScript {
			t.Errorf("created route = %v, want shop.app.com/* on %s", m.createdRoutes[0], domainEntryScript)
		}
		if len(m.createdRecords) != 0 {
			t.Errorf("created records = %v, want none: binding a domain plants nothing", m.createdRecords)
		}
		if got := edge.BoundDomains(s.State()); len(got) != 1 || got[0] != "shop.app.com" {
			t.Errorf("bound domains = %v, want [shop.app.com]", got)
		}
	})

	t.Run("a host deeper than Universal SSL with no certificate pack is refused", func(t *testing.T) {
		m := zoneMock()
		s := domainStack(t, m)

		err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "eu.shop.app.com"})
		if err == nil {
			t.Fatal("BindDomain err = nil, want a refusal: nothing terminates TLS there")
		}
		if !strings.Contains(err.Error(), "Advanced Certificate") {
			t.Errorf("BindDomain err = %q, want it to name the Advanced Certificate to add", err)
		}
		if len(m.createdRoutes) != 0 || len(m.createdRecords) != 0 {
			t.Errorf("created routes = %v and records = %v, want a refusal to leave nothing behind", m.createdRoutes, m.createdRecords)
		}
		if got := edge.BoundDomains(s.State()); len(got) != 0 {
			t.Errorf("bound domains = %v, want none after a refusal", got)
		}
	})

	t.Run("a certificate pack covering the host lets it bind", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			hosts []string
		}{
			{name: "named exactly", hosts: []string{"app.com", "eu.shop.app.com"}},
			{name: "under a wildcard", hosts: []string{"*.shop.app.com"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m := zoneMock()
				m.certificatePacks = []map[string]any{{"id": "pack1", "status": "active", "hosts": tc.hosts}}
				s := domainStack(t, m)

				if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "eu.shop.app.com"}); err != nil {
					t.Fatalf("BindDomain: %v", err)
				}
				if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "eu.shop.app.com/*" {
					t.Errorf("created routes = %v, want the covered host's route", m.createdRoutes)
				}
			})
		}
	})

	t.Run("a certificate pack that is not active covers nothing", func(t *testing.T) {
		m := zoneMock()
		m.certificatePacks = []map[string]any{{"id": "pack1", "status": "pending_validation", "hosts": []string{"eu.shop.app.com"}}}
		s := domainStack(t, m)

		if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "eu.shop.app.com"}); err == nil {
			t.Fatal("BindDomain err = nil, want a refusal while the certificate pack is still validating")
		}
	})

	t.Run("an existing record that is not proxied is refused", func(t *testing.T) {
		m := zoneMock()
		m.existingRecords = []map[string]any{
			{"id": "own", "name": "shop.app.com", "type": "A", "content": "203.0.113.1", "proxied": false},
		}
		s := domainStack(t, m)

		err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"})
		if err == nil {
			t.Fatal("BindDomain err = nil, want a refusal: no worker route fires behind a grey cloud")
		}
		if !strings.Contains(err.Error(), "proxied") {
			t.Errorf("BindDomain err = %q, want it to name the proxying the record lacks", err)
		}
		if len(m.createdRecords) != 0 {
			t.Errorf("created records = %v, want the user's own record left alone", m.createdRecords)
		}
	})

	t.Run("a stack that reports no entry worker refuses to bind", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		s := stackOn(zoneMock().provider(t), edge.StackState{edge.StackKeySlug: "acme-web"})

		if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"}); err == nil {
			t.Fatal("BindDomain err = nil, want a refusal while no worker serves the stack")
		}
	})

	t.Run("the shared preview entry worker refuses to carry a project's domain", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		m := zoneMock()
		s := stackOn(m.provider(t), edge.StackState{
			edge.StackKeySlug:   "acme-web",
			stackKeyEntryWorker: previewEntryScript,
		})

		err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"})
		if err == nil {
			t.Fatal("BindDomain err = nil, want a refusal to route a customer hostname at the multi-tenant worker")
		}
		if !strings.Contains(err.Error(), previewEntryScript) {
			t.Errorf("BindDomain err = %q, want it to name the shared worker", err)
		}
		if len(m.createdRoutes) != 0 || len(m.createdRecords) != 0 {
			t.Errorf("created routes = %v and records = %v, want a refusal to leave nothing behind", m.createdRoutes, m.createdRecords)
		}
	})

	t.Run("a stack serving several apps refuses to pick one for the host", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)

		state := testState(store.URL, "s3cr3t")
		for _, name := range []string{"ocel-preview--web", "ocel-preview--api"} {
			spec := previewSpec(store.URL, "v2")
			spec.Program.Name = name
			opened, err := p.Reconcile(t.Context(), spec, state)
			if err != nil {
				t.Fatalf("Reconcile(%s): %v", name, err)
			}
			state = opened.State()
		}

		err := stackOn(p, state).BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"})
		if err == nil {
			t.Fatal("BindDomain err = nil, want a refusal: nothing says which app's worker should answer")
		}
		for _, name := range []string{"ocel-preview--web", "ocel-preview--api"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("BindDomain err = %q, want it to name %q", err, name)
			}
		}
	})

	t.Run("a hostname no zone in the account owns is refused", func(t *testing.T) {
		m := zoneMock()
		s := domainStack(t, m)

		if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.elsewhere.com"}); err == nil {
			t.Fatal("BindDomain err = nil, want a refusal for a hostname outside the account's zones")
		}
	})
}

func TestUnbindDomain(t *testing.T) {
	t.Run("takes the route and leaves the zone's records alone", func(t *testing.T) {
		m := zoneMock()
		m.existingRoutes = []map[string]any{
			{"id": "bound", "pattern": "shop.app.com/*", "script": domainEntryScript},
		}
		m.existingRecords = []map[string]any{
			{"id": "placeholder", "name": "shop.app.com", "type": "AAAA", "content": edge.ProxyPlaceholder, "comment": recordComment, "proxied": true},
		}
		s := domainStack(t, m)
		s.state = edge.RecordBoundDomain(s.state, "shop.app.com")

		if err := s.UnbindDomain(t.Context(), "shop.app.com"); err != nil {
			t.Fatalf("UnbindDomain: %v", err)
		}

		assertSet(t, "deleted routes", m.deletedRoutes, []string{"bound"})
		assertSet(t, "deleted records", m.deletedRecords, nil)
		if got := edge.BoundDomains(s.State()); len(got) != 0 {
			t.Errorf("bound domains = %v, want none", got)
		}
	})

	t.Run("leaves another worker's route where it stands", func(t *testing.T) {
		m := zoneMock()
		m.existingRoutes = []map[string]any{
			{"id": "theirs", "pattern": "shop.app.com/*", "script": "someone-else"},
		}
		s := domainStack(t, m)

		if err := s.UnbindDomain(t.Context(), "shop.app.com"); err != nil {
			t.Fatalf("UnbindDomain: %v", err)
		}
		if len(m.deletedRoutes) != 0 {
			t.Errorf("deleted routes = %v, want a route this stack never owned untouched", m.deletedRoutes)
		}
	})

}

func TestReconcileRecordsItsEntryWorker(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	spec := previewSpec(store.URL, "v2")

	state, err := reconcileState(t, previewZoneMock().provider(t), spec, testState(store.URL, "s3cr3t"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if state[stackKeyEntryWorker] != spec.Program.Name {
		t.Errorf("recorded entry worker = %q, want the worker a bound domain routes to (%q)", state[stackKeyEntryWorker], spec.Program.Name)
	}
}

func TestReconcileKeepsWhatABindingPut(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	spec := previewSpec(store.URL, "v2")

	bound, err := p.Reconcile(t.Context(), spec, testState(store.URL, "s3cr3t"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := bound.BindDomain(t.Context(), edge.DomainBinding{Hostname: "shop.app.com"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	redeployed, err := p.Reconcile(t.Context(), previewSpec(store.URL, "v3"), bound.State())
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	if !hasRoute(m, "shop.app.com/*") {
		t.Errorf("routes = %v, want the bound host's route to survive the next deploy", m.existingRoutes)
	}
	if got := edge.BoundDomains(redeployed.State()); len(got) != 1 || got[0] != "shop.app.com" {
		t.Errorf("bound domains = %v, want the deploy to carry [shop.app.com] forward", got)
	}

	if err := redeployed.Destroy(t.Context()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if hasRoute(m, "shop.app.com/*") {
		t.Errorf("routes = %v, want the bound host's route gone with the stack", m.existingRoutes)
	}
}

func TestDestroyOutlivesAnUnbindThatCannotRun(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	m := previewZoneMock()
	p := m.provider(t)
	putStampSet(t, p, store.URL, "s3cr3t", stampSet{"ocel-preview": "v1"})

	state := testState(store.URL, "s3cr3t")
	state[stackKeyEntryWorker] = "ocel-preview"
	state = edge.RecordBoundDomain(state, "shop.elsewhere.com")

	err := stackOn(p, state).Destroy(t.Context())
	if err == nil {
		t.Fatal("Destroy err = nil, want the unbind that could not run reported")
	}
	if !slices.Contains(m.deletedScripts, "ocel-preview") {
		t.Errorf("deleted scripts = %v, want the workers destroyed even so", m.deletedScripts)
	}
	if _, err := stackOn(p, state).History(t.Context(), ""); err == nil {
		t.Error("history after Destroy: err = nil, want the store instance gone even so")
	}
}

func TestPruneOnlyRecordsNoEntryWorker(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	store := fakeStoreServer(t, "s3cr3t")
	spec := pruneOnlySpec(store.URL, "v2")

	prior := testState(store.URL, "s3cr3t")
	prior[stackKeyEntryWorker] = spec.Program.Name

	state, err := reconcileState(t, previewZoneMock().provider(t), spec, prior)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if state[stackKeyEntryWorker] != "" {
		t.Errorf("recorded entry worker = %q, want none: the script it names was just deleted", state[stackKeyEntryWorker])
	}
}

func hasRoute(m *cfMock, pattern string) bool {
	return slices.ContainsFunc(m.existingRoutes, func(route map[string]any) bool {
		return route["pattern"] == pattern
	})
}

func TestCertificateCovers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		covered  string
		hostname string
		want     bool
	}{
		{"the host itself", "eu.shop.app.com", "eu.shop.app.com", true},
		{"a different host", "de.shop.app.com", "eu.shop.app.com", false},
		{"a wildcard one label up", "*.shop.app.com", "eu.shop.app.com", true},
		{"a wildcard two labels up", "*.app.com", "eu.shop.app.com", false},
		{"a wildcard over the apex itself", "*.app.com", "app.com", false},
		{"a wildcard matching a wildcard host", "*.shop.app.com", "*.shop.app.com", true},
		{"an unrelated zone's wildcard", "*.shop.other.com", "eu.shop.app.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := certificateCovers(tc.covered, tc.hostname); got != tc.want {
				t.Errorf("certificateCovers(%q, %q) = %v, want %v", tc.covered, tc.hostname, got, tc.want)
			}
		})
	}
}
