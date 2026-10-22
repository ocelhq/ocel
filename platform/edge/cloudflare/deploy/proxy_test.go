package cloudflare

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func proxyZoneMock() *cfMock {
	return &cfMock{zoneID: "zone1", zoneName: "app.com"}
}

func proxySpec() edge.StackSpec {
	return edge.StackSpec{Tier: environment.TierProduction, Slug: "acme", Domains: []string{"shop.app.com"}}
}

func (m *cfMock) proxy(t *testing.T) *Proxy {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	return &Proxy{p: m.provider(t)}
}

func reconciledProxy(t *testing.T, m *cfMock) (*Proxy, edge.EdgeStack) {
	t.Helper()
	front := m.proxy(t)
	stack, err := front.Reconcile(context.Background(), proxySpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return front, stack
}

func TestTheCloudflareProxyBehavesAsEveryEdgeMust(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			return proxyZoneMock().proxy(t), proxySpec()
		},
		Hostname: "shop.app.com",
		Origin:   &edge.Origin{Address: "198.51.100.4"},
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			m := proxyZoneMock()
			m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
			return m.proxy(t), proxyPreviewSpec(), edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}
		},
		Bootstrap: func(t *testing.T) (edge.Edge, environment.Tier) {
			t.Setenv(envAPIToken, "tok")
			return proxyZoneMock().proxy(t), environment.TierProduction
		},
	})
}

func TestTheCloudflareProxyRunsNoCodeAndProxiesTheRecordsItWrites(t *testing.T) {
	facts := proxyZoneMock().proxy(t).Facts()
	if facts.RunsCode {
		t.Errorf("Facts() = %+v, want no code run: the proxy forwards to an origin and runs no worker", facts)
	}
	if !facts.ProxiesRecords || facts.ServesUnbound {
		t.Errorf("Facts() = %+v, want records proxied to an origin and no hostname answered unbound", facts)
	}
	if !facts.ShieldsOrigin {
		t.Errorf("Facts() = %+v, want the origin shielded: it answers only what arrives with the client certificate the zone presents", facts)
	}
}

func TestTheCloudflareProxyAloneDoesNotClaimToGate(t *testing.T) {
	if proxyZoneMock().proxy(t).Facts().Gates {
		t.Error("Facts().Gates = true, want false: the proxy runs no code, so only an origin behind it that runs the gate can claim it")
	}
}

func TestTheCloudflareProxyWritesOneProxiedRecordToTheOriginItForwardsTo(t *testing.T) {
	m := proxyZoneMock()
	_, stack := reconciledProxy(t, m)

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if len(m.createdRecords) != 1 {
		t.Fatalf("created records %v, want the one proxied record", m.createdRecords)
	}
	written := m.createdRecords[0]
	if written["type"] != "A" || written["content"] != "198.51.100.4" || written["proxied"] != true {
		t.Errorf("wrote %v, want a proxied A record at 198.51.100.4", written)
	}
	if want := stack.State().Records; len(want) != 1 || want[0].Name != "shop.app.com" {
		t.Errorf("the stack records it wrote %v, want the record at shop.app.com, so no DNS writer asks it of anyone again", want)
	}

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.9"}}); err != nil {
		t.Fatalf("BindDomain onto a moved origin: %v", err)
	}
	if len(m.createdRecords) != 1 || len(m.updatedRecords) != 1 {
		t.Errorf("created %d and updated %d records, want the one record repointed at the moved origin", len(m.createdRecords), len(m.updatedRecords))
	}
}

func TestTheCloudflareProxyRepointsAHostnameItForwardsTwiceAtOneRecordAndDeletesTheOther(t *testing.T) {
	m := proxyZoneMock()
	owned := formatOwnerComment(mustFormatForwardingOwner(defaultNamespace, testOrigin, "acme", environment.TierProduction))
	m.existingRecords = []map[string]any{
		{"id": "first", "name": "shop.app.com", "type": "A", "content": "198.51.100.4", "proxied": true, "comment": owned},
		{"id": "second", "name": "shop.app.com", "type": "A", "content": "198.51.100.4", "proxied": true, "comment": owned},
	}
	_, stack := reconciledProxy(t, m)

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.9"}}); err != nil {
		t.Fatalf("BindDomain onto a moved origin: %v", err)
	}
	if len(m.existingRecords) != 1 || m.existingRecords[0]["content"] != "198.51.100.9" {
		t.Errorf("the zone holds %v for shop.app.com, want one record at the moved origin: a record left at the old origin keeps forwarding there", m.existingRecords)
	}
}

func TestTheCloudflareProxyRefusesAZoneThatDoesNotValidateTheOriginsCertificate(t *testing.T) {
	for _, mode := range []string{"off", "flexible", "full"} {
		m := proxyZoneMock()
		m.sslMode = mode
		_, stack := reconciledProxy(t, m)

		err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", mode)) || !strings.Contains(err.Error(), "Full (strict)") {
			t.Errorf("BindDomain in a zone whose SSL mode is %s = %v, want it refused naming the mode and Full (strict): short of strict, Cloudflare forwards to whatever answers the origin's address, with no certificate checked", mode, err)
		}
		if len(m.createdRecords) != 0 {
			t.Errorf("SSL mode %s: wrote %v, want nothing forwarded", mode, m.createdRecords)
		}
	}
}

func TestTheCloudflareProxyForwardsThroughAZoneThatValidatesTheOriginsCertificate(t *testing.T) {
	for _, mode := range []string{"strict", "origin_pull"} {
		m := proxyZoneMock()
		m.sslMode = mode
		_, stack := reconciledProxy(t, m)
		if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
			t.Errorf("BindDomain in a zone whose SSL mode is %s = %v, want it forwarded", mode, err)
		}
	}
}

func TestTheCloudflareProxyRefusesAHostnameItHasNoOriginFor(t *testing.T) {
	m := proxyZoneMock()
	_, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com"})
	if err == nil {
		t.Fatal("BindDomain with no origin = nil, want a refusal: the proxy answers nothing itself")
	}
	if strings.Contains(err.Error(), "router") {
		t.Errorf("BindDomain with no origin = %v, want no router named: the router is never yours to configure", err)
	}
	_, err = m.proxy(t).ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: "preview.app.com"})
	if err == nil || strings.Contains(err.Error(), "router") {
		t.Errorf("ReconcilePreviewWildcard with no origin = %v, want a refusal naming no router", err)
	}
}

func TestTheCloudflareProxyLeavesARecordItDidNotWrite(t *testing.T) {
	m := proxyZoneMock()
	m.existingRecords = []map[string]any{{"id": "theirs", "name": "shop.app.com", "type": "A", "content": "192.0.2.1", "proxied": false}}
	_, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatalf("BindDomain over a record ocel did not write = %v, want it refused and the record named", err)
	}
	if len(m.updatedRecords) != 0 || len(m.deletedRecords) != 0 {
		t.Errorf("updated %v and deleted %v, want the record someone else wrote left alone", m.updatedRecords, m.deletedRecords)
	}
}

func TestTheCloudflareProxyRefusesAHostnameAnotherProjectForwards(t *testing.T) {
	m := proxyZoneMock()
	m.existingRecords = []map[string]any{{
		"id": "other", "name": "shop.app.com", "type": "A", "content": "192.0.2.1", "proxied": true,
		"comment": formatOwnerComment(mustFormatForwardingOwner(defaultNamespace, testOrigin, "other", environment.TierProduction)),
	}}
	front, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("BindDomain over another project's forward = %v, want it refused naming that project", err)
	}
	if err := stack.UnbindDomain(context.Background(), "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted %v, want another project's record left in place", m.deletedRecords)
	}
	owner, err := front.DomainOwner(context.Background(), "shop.app.com")
	if err != nil || owner != mustFormatForwardingOwner(defaultNamespace, testOrigin, "other", environment.TierProduction) {
		t.Errorf("DomainOwner = %q, %v, want the project that forwards it", owner, err)
	}
}

func TestTheCloudflareProxyRefusesAHostnameTheSameProjectForwardsOnAnotherOrigin(t *testing.T) {
	m := proxyZoneMock()
	theirs := mustFormatForwardingOwner(defaultNamespace, otherOrigin, "acme", environment.TierProduction)
	m.existingRecords = []map[string]any{{
		"id": "other-origin", "name": "shop.app.com", "type": "A", "content": "192.0.2.1", "proxied": true,
		"comment": formatOwnerComment(theirs),
	}}
	front, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), otherOrigin.String()) || !strings.Contains(err.Error(), "one hostname belongs to one origin") {
		t.Fatalf("BindDomain over acme's forward on %s = %v, want it refused naming that origin", otherOrigin, err)
	}
	if len(m.updatedRecords) != 0 || len(m.createdRecords) != 0 {
		t.Errorf("updated %v and created %v, want %s's record neither adopted nor repointed", m.updatedRecords, m.createdRecords, otherOrigin)
	}
	if err := stack.UnbindDomain(context.Background(), "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted %v, want %s's record left in place", m.deletedRecords, otherOrigin)
	}
	owner, err := front.DomainOwner(context.Background(), "shop.app.com")
	mine, _ := front.ProjectOwner(context.Background(), "acme", environment.TierProduction)
	if err != nil || owner != theirs || owner == mine {
		t.Errorf("DomainOwner = %q, %v, want %q, which acme on this origin (%q) does not own", owner, err, theirs, mine)
	}
}

func TestTheCloudflareProxyRefusesAHostnameAnotherOriginsDNSWriterWrote(t *testing.T) {
	m := proxyZoneMock()
	m.existingRecords = []map[string]any{{
		"id": "other-origin", "name": "shop.app.com", "type": "A", "content": "192.0.2.1", "proxied": true,
		"comment": formatWriterComment(otherOrigin),
	}}
	_, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), otherOrigin.String()) || !strings.Contains(err.Error(), "one hostname belongs to one origin") {
		t.Fatalf("BindDomain over a record %s wrote = %v, want it refused naming that origin", otherOrigin, err)
	}
	if len(m.updatedRecords) != 0 || len(m.deletedRecords) != 0 {
		t.Errorf("updated %v and deleted %v, want %s's record left alone", m.updatedRecords, m.deletedRecords, otherOrigin)
	}
}

func TestTheCloudflareProxyAdoptsARecordThisOriginsDNSWriterWrote(t *testing.T) {
	m := proxyZoneMock()
	m.existingRecords = []map[string]any{{
		"id": "written", "name": "shop.app.com", "type": "AAAA", "content": edge.ProxyPlaceholder, "proxied": true,
		"comment": formatWriterComment(testOrigin),
	}}
	_, stack := reconciledProxy(t, m)

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("BindDomain over a record this origin wrote: %v", err)
	}
	want := formatOwnerComment(mustFormatForwardingOwner(defaultNamespace, testOrigin, "acme", environment.TierProduction))
	if len(m.existingRecords) != 1 || m.existingRecords[0]["content"] != "198.51.100.4" || m.existingRecords[0]["comment"] != want {
		t.Errorf("the zone holds %v, want the record repointed at the origin and owned by acme on %s", m.existingRecords, testOrigin)
	}
}

func proxyPreviewSpec() edge.StackSpec {
	spec := proxySpec()
	spec.Tier, spec.Domains = environment.TierPreview, []string{"*.preview.app.com"}
	return spec
}

func activePack(hosts ...string) map[string]any {
	return map[string]any{"id": "pack", "type": "advanced", "status": "active", "hosts": hosts}
}

func TestTheCloudflareProxyForwardsAProjectsPreviewWildcardToTheOriginItsRouterClaims(t *testing.T) {
	m := proxyZoneMock()
	m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
	stack, err := m.proxy(t).Reconcile(context.Background(), proxyPreviewSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile of a preview wildcard: %v", err)
	}
	if base := stack.State().PreviewBase; base != "preview.app.com" {
		t.Errorf("the stack serves previews on %q, want preview.app.com: the router behind the proxy answers each preview under it", base)
	}

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("BindDomain(*.preview.app.com): %v", err)
	}
	if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "*.preview.app.com" || m.createdRecords[0]["proxied"] != true {
		t.Errorf("created records %v, want one proxied wildcard record forwarding every preview to the origin", m.createdRecords)
	}
}

func TestTheCloudflareProxyOrdersAnAdvancedCertificateForAPreviewWildcardUniversalSSLDoesNotCover(t *testing.T) {
	m := proxyZoneMock()
	stack, err := m.proxy(t).Reconcile(context.Background(), proxyPreviewSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	err = stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("BindDomain before the zone covers the wildcard = %v, want refused not ready while Cloudflare issues its certificate", err)
	}
	if len(m.orderedPacks) != 1 || !slices.Equal(m.orderedPacks[0], []string{"app.com", "*.preview.app.com"}) {
		t.Fatalf("ordered certificate packs %v, want one advanced certificate for app.com and *.preview.app.com", m.orderedPacks)
	}
	if len(m.createdRecords) != 0 {
		t.Errorf("created %v before any certificate covers the wildcard, want nothing forwarded: every preview would fail its handshake at Cloudflare", m.createdRecords)
	}

	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err == nil {
		t.Error("BindDomain while the ordered certificate is pending = nil, want it still not ready")
	}
	if len(m.orderedPacks) != 1 {
		t.Errorf("ordered %d certificate packs, want the one already pending left to finish", len(m.orderedPacks))
	}

	m.certificatePacks[0]["status"] = "active"
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Errorf("BindDomain once the certificate is active: %v", err)
	}
}

func TestTheCloudflareProxyRefusesAPreviewWildcardAZoneWithoutAdvancedCertificateManagerCannotCover(t *testing.T) {
	m := proxyZoneMock()
	m.refusesPackOrders = true
	stack, err := m.proxy(t).Reconcile(context.Background(), proxyPreviewSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	err = stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), "Advanced Certificate Manager") || !strings.Contains(err.Error(), "zone apex") {
		t.Errorf("BindDomain in a zone that cannot order an advanced certificate = %v, want a refusal naming Advanced Certificate Manager and a preview domain at a zone apex, which Universal SSL covers", err)
	}
}

func TestTheCloudflareProxyNeedsNoAdvancedCertificateForAPreviewWildcardAtAZoneApex(t *testing.T) {
	m := &cfMock{zoneID: "zone1", zoneName: "previews.app"}
	spec := proxyPreviewSpec()
	spec.Domains = []string{"*.previews.app"}
	stack, err := m.proxy(t).Reconcile(context.Background(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "*.previews.app", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("BindDomain(*.previews.app): %v", err)
	}
	if len(m.orderedPacks) != 0 {
		t.Errorf("ordered %v, want nothing: Universal SSL covers every name one label below the zone", m.orderedPacks)
	}
}

func TestTheCloudflareProxyForwardsTheSharedPreviewWildcardToTheOriginItsRouterClaims(t *testing.T) {
	m := proxyZoneMock()
	m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
	front := m.proxy(t)
	ctx := context.Background()

	if _, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: "preview.app.com"}); err == nil {
		t.Error("ReconcilePreviewWildcard with no origin = nil, want a refusal: the proxy answers no preview itself")
	}
	published, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	if published != "198.51.100.4" {
		t.Errorf("ReconcilePreviewWildcard published %q, want the origin it forwards to", published)
	}
	owner, err := front.DomainOwner(ctx, "*.preview.app.com")
	if err != nil || owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(*.preview.app.com) = %q, %v, want %q: the wildcard is every project's preview entry", owner, err, edge.PreviewEntryOwner)
	}
	if err := front.DestroyPreviewWildcard(ctx, "preview.app.com"); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if len(m.deletedRecords) != 1 {
		t.Errorf("deleted %v, want the wildcard's proxied record", m.deletedRecords)
	}
}

func TestTheCloudflareProxyRefusesThePreviewWildcardAnotherOriginForwards(t *testing.T) {
	m := proxyZoneMock()
	m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
	theirs := formatPreviewForwardingOwner(otherOrigin)
	m.existingRecords = []map[string]any{{
		"id": "other-origin", "name": "*.preview.app.com", "type": "A", "content": "192.0.2.1", "proxied": true,
		"comment": formatOwnerComment(theirs),
	}}
	front := m.proxy(t)
	ctx := context.Background()

	_, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
	if err == nil || !strings.Contains(err.Error(), otherOrigin.String()) {
		t.Fatalf("ReconcilePreviewWildcard over %s's wildcard = %v, want it refused naming that origin", otherOrigin, err)
	}
	if len(m.updatedRecords) != 0 || len(m.createdRecords) != 0 {
		t.Errorf("updated %v and created %v, want %s's wildcard record left forwarding to its origin", m.updatedRecords, m.createdRecords, otherOrigin)
	}
	owner, err := front.DomainOwner(ctx, "*.preview.app.com")
	if err != nil || owner != theirs {
		t.Errorf("DomainOwner(*.preview.app.com) = %q, %v, want %q: the wildcard is %s's preview entry, not this origin's", owner, err, theirs, otherOrigin)
	}
	if err := front.DestroyPreviewWildcard(ctx, "preview.app.com"); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if len(m.deletedRecords) != 0 {
		t.Errorf("deleted %v, want %s's wildcard record left in place", m.deletedRecords, otherOrigin)
	}
}

func TestTheCloudflareProxyIssuesAnOriginCertificateForAHostnameAndRevokesIt(t *testing.T) {
	m := proxyZoneMock()
	hooks := m.proxy(t).Hooks().OriginCertificates
	ctx := context.Background()

	issued, err := hooks.Issue(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(m.originRequests) != 1 || !slices.Equal(m.originRequests[0], []string{"shop.app.com"}) {
		t.Errorf("asked Cloudflare's origin CA for %v, want a certificate for shop.app.com alone", m.originRequests)
	}
	block, _ := pem.Decode([]byte(m.originCSRs[0]))
	if block == nil {
		t.Fatalf("the request %q is no PEM", m.originCSRs[0])
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(request.Subject.Organization, []string{"ocel"}) {
		t.Errorf("the request names organization %v, want ocel: the certificate lists nothing else that says ocel asked for it, and a sweep revokes only what ocel asked for", request.Subject.Organization)
	}
	leaf := parsedLeaf(t, issued.Certificate)
	if !issued.ExpiresAt.Equal(leaf.NotAfter) {
		t.Errorf("the certificate is read as expiring %s, want %s, the moment its leaf says", issued.ExpiresAt, leaf.NotAfter)
	}
	if lifetime := time.Until(leaf.NotAfter); lifetime > 400*24*time.Hour {
		t.Errorf("the origin certificate is good for %s, want about a year: it is renewed half way through", lifetime)
	}

	if err := hooks.Revoke(ctx, issued.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !slices.Equal(m.revokedOrigin, []string{issued.ID}) {
		t.Errorf("revoked %v, want %s", m.revokedOrigin, issued.ID)
	}
}

func parsedLeaf(t *testing.T, certificate string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certificate))
	if block == nil {
		t.Fatalf("%q is no PEM", certificate)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %q: %v", certificate, err)
	}
	return leaf
}

func TestTheCloudflareProxyPurgesHostnamesAHundredAtATime(t *testing.T) {
	m := proxyZoneMock()
	hostnames := make([]string, 0, 150)
	for i := range 150 {
		hostnames = append(hostnames, fmt.Sprintf("h%d.app.com", i))
	}

	if err := m.proxy(t).Hooks().PurgeHostnames(context.Background(), hostnames); err != nil {
		t.Fatalf("PurgeHostnames: %v", err)
	}
	if len(m.purges) != 2 || len(m.purges[0]) != 100 || len(m.purges[1]) != 50 {
		t.Errorf("purged in batches of %v, want 100 then 50: Cloudflare purges at most 100 hostnames a request", batchSizes(m.purges))
	}
}

func batchSizes(batches [][]string) []int {
	sizes := make([]int, 0, len(batches))
	for _, batch := range batches {
		sizes = append(sizes, len(batch))
	}
	return sizes
}
