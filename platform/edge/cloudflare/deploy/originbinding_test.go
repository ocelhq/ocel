package cloudflare

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const albAddress = "ocel-alb-123.us-east-1.elb.amazonaws.com"

func originStack(t *testing.T, m *cfMock, tier environment.Tier) *stack {
	t.Helper()
	s := domainStack(t, m)
	s.state.Tier = tier
	return s
}

func TestAProductionHostnameBoundWithAnOriginIsForwardedThroughAProxiedRecordAndRunsNoWorker(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierProduction)

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.createdRecords) != 1 {
		t.Fatalf("created records = %v, want the one proxied record forwarding api.app.com", m.createdRecords)
	}
	written := m.createdRecords[0]
	if written["type"] != "CNAME" || written["content"] != albAddress || written["proxied"] != true {
		t.Errorf("wrote %v, want a proxied CNAME to %s", written, albAddress)
	}
	for _, route := range m.createdRoutes {
		if script, named := route["script"]; named && script != "" {
			t.Errorf("created route %v runs %v, want no worker: Cloudflare forwards the hostname to its origin, and a worker route would run the entry worker in front of it", route, script)
		}
	}
	if got := s.State().Bound; len(got) != 1 || got[0] != "api.app.com" {
		t.Errorf("bound domains = %v, want [api.app.com]", got)
	}
	if got := s.State().Records; len(got) != 1 || got[0].Name != "api.app.com" {
		t.Errorf("the stack records it wrote %v, want the forwarding record, so no DNS writer is asked for it again", got)
	}
}

func TestAHostnameMovedOntoAnOriginDropsTheWorkerRouteThatServedIt(t *testing.T) {
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "served", "pattern": "api.app.com/*", "script": domainEntryScript}}
	s := originStack(t, m, environment.TierProduction)

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	assertSet(t, "deleted routes", m.deletedRoutes, []string{"served"})
}

func TestAProductionHostnameBoundWithAnOriginUnderAWildcardWorkerRouteGetsAnExactRouteThatRunsNoWorker(t *testing.T) {
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "wildcard", "pattern": "*.app.com/*", "script": "user-wildcard"}}
	s := originStack(t, m, environment.TierProduction)

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "api.app.com/*" {
		t.Fatalf("created routes = %v, want api.app.com/*, which beats the wildcard route that would otherwise run its worker in front of the origin", m.createdRoutes)
	}
	if script, named := m.createdRoutes[0]["script"]; named && script != "" {
		t.Errorf("created route %v runs %v, want no worker", m.createdRoutes[0], script)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want the wildcard route left serving every other host", m.deletedRoutes)
	}
}

func TestUnbindingAProductionHostnameForwardedToAnOriginRemovesItsRouteThatRunsNoWorker(t *testing.T) {
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "wildcard", "pattern": "*.app.com/*", "script": "user-wildcard"}}
	s := originStack(t, m, environment.TierProduction)
	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if err := s.UnbindDomain(t.Context(), "api.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if len(m.existingRoutes) != 1 || m.existingRoutes[0]["id"] != "wildcard" {
		t.Errorf("the zone holds routes %v, want only the wildcard route", m.existingRoutes)
	}
}

func TestAPreviewHostBoundWithAnOriginGetsAnExactRecordAndARouteThatRunsNoWorker(t *testing.T) {
	m := zoneMock()
	m.existingRoutes = []map[string]any{{"id": "wildcard", "pattern": "*.app.com/*", "script": domainEntryScript}}
	s := originStack(t, m, environment.TierPreview)

	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api-pr-7.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "api-pr-7.app.com" || m.createdRecords[0]["proxied"] != true {
		t.Fatalf("created records = %v, want one proxied record at api-pr-7.app.com, which beats the wildcard record", m.createdRecords)
	}
	if len(m.createdRoutes) != 1 {
		t.Fatalf("created routes = %v, want one route for the preview host", m.createdRoutes)
	}
	route := m.createdRoutes[0]
	if route["pattern"] != "api-pr-7.app.com/*" {
		t.Errorf("created route %v, want api-pr-7.app.com/*", route)
	}
	if script, named := route["script"]; named && script != "" {
		t.Errorf("created route %v runs %v, want no script: the more specific route keeps the wildcard's worker off the host its origin answers", route, script)
	}
	if len(m.deletedRoutes) != 0 {
		t.Errorf("deleted routes = %v, want the wildcard's route left serving every other preview", m.deletedRoutes)
	}
}

func TestUnbindingAPreviewHostForwardedToAnOriginRemovesItsRecordAndItsRoute(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierPreview)
	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api-pr-7.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if err := s.UnbindDomain(t.Context(), "api-pr-7.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if len(m.existingRecords) != 0 {
		t.Errorf("the zone still holds %v, want the forwarding record removed", m.existingRecords)
	}
	if len(m.existingRoutes) != 0 {
		t.Errorf("the zone still holds routes %v, want the route that runs no worker removed", m.existingRoutes)
	}
	if got := s.State(); len(got.Bound) != 0 || len(got.Records) != 0 {
		t.Errorf("the stack still records bound %v and records %v, want neither", got.Bound, got.Records)
	}
}

func TestTheRouteThatRunsNoWorkerNamesNoOwnerInPlaceOfTheRecordThatForwardsTheHost(t *testing.T) {
	m := zoneMock()
	s := originStack(t, m, environment.TierPreview)
	if err := s.BindDomain(t.Context(), edge.DomainBinding{Hostname: "api-pr-7.app.com", Origin: &edge.Origin{Address: albAddress, Certified: true}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	owner, err := s.p.DomainOwner(t.Context(), "api-pr-7.app.com")
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if want := formatForwardingOwner(defaultNamespace, "acme-web", environment.TierPreview); owner != want {
		t.Errorf("DomainOwner = %q, want %q, the owner the forwarding record names", owner, want)
	}
}

func TestTheWorkerEdgesTokenMayEditTheZonesClientCertificatesAndReadItsSSLMode(t *testing.T) {
	for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
		document, err := credentialPermissions(purpose)
		if err != nil {
			t.Fatalf("credentialPermissions(%s): %v", purpose, err)
		}
		for _, needed := range []string{"Zone · SSL and Certificates · Edit", "Zone · Zone Settings · Read"} {
			if !strings.Contains(document.Document, needed) {
				t.Errorf("the %s token lists\n%s\nwant %q: forwarding a hostname to an origin reads the zone's SSL mode and uploads its client certificate", purpose, document.Document, needed)
			}
		}
	}
}

func TestBothCloudflareEdgesNameTheRangesCloudflareReachesAnOriginFrom(t *testing.T) {
	m := zoneMock()
	for name, facts := range map[string]edge.Facts{"worker": m.provider(t).Facts(), "proxy": m.proxy(t).Facts()} {
		if !slices.Contains(facts.OriginFacingRanges, "173.245.48.0/20") || !slices.Contains(facts.OriginFacingRanges, "131.0.72.0/22") {
			t.Errorf("the %s edge names origin-facing ranges %v, want the ranges Cloudflare publishes at cloudflare.com/ips-v4", name, facts.OriginFacingRanges)
		}
	}
}

func TestTheWorkerEdgePresentsTheZonesClientCertificateToTheOriginsItForwardsTo(t *testing.T) {
	m := zoneMock()
	hooks := m.provider(t).Hooks()
	if hooks.ClientCertificates == nil {
		t.Fatal("Hooks().ClientCertificates = nil, want the zone's client certificates, which an origin behind a forwarded hostname trusts and nothing else")
	}
}
