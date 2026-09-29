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
			return proxyZoneMock().proxy(t), environment.TierProduction
		},
	})
}

func TestTheCloudflareProxyRunsNoCodeAndProxiesTheRecordsItWrites(t *testing.T) {
	facts := proxyZoneMock().proxy(t).Facts()
	if facts.RunsCode || !facts.Compatibility.IsZero() {
		t.Errorf("Facts() = %+v, want no code run: the proxy forwards to an origin and runs no worker", facts)
	}
	if !facts.ProxiesRecords || facts.ServesUnbound {
		t.Errorf("Facts() = %+v, want records proxied to an origin and no hostname answered unbound", facts)
	}
	if !facts.ShieldsOrigin {
		t.Errorf("Facts() = %+v, want the origin shielded: it answers only what arrives with the client certificate the zone presents", facts)
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

func TestTheCloudflareProxyRefusesAZoneThatReachesOriginsOverPlainHTTP(t *testing.T) {
	for _, mode := range []string{"off", "flexible"} {
		m := proxyZoneMock()
		m.sslMode = mode
		_, stack := reconciledProxy(t, m)

		err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}})
		if err == nil || !strings.Contains(err.Error(), mode) {
			t.Errorf("BindDomain in a zone whose SSL mode is %s = %v, want it refused naming the mode: the origin answers only TLS carrying the zone's client certificate, which plain HTTP never presents", mode, err)
		}
		if len(m.createdRecords) != 0 {
			t.Errorf("SSL mode %s: wrote %v, want nothing forwarded", mode, m.createdRecords)
		}
	}
}

func TestTheCloudflareProxyRefusesAHostnameItHasNoOriginFor(t *testing.T) {
	_, stack := reconciledProxy(t, proxyZoneMock())

	err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.app.com"})
	if err == nil {
		t.Fatal("BindDomain with no origin = nil, want a refusal: the proxy answers nothing itself")
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
		"comment": ownerComment(forwardingOwner(defaultNamespace, "other", environment.TierProduction)),
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
	if err != nil || owner != forwardingOwner(defaultNamespace, "other", environment.TierProduction) {
		t.Errorf("DomainOwner = %q, %v, want the project that forwards it", owner, err)
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

func TestTheCloudflareProxyMintsAClientCertificateOnlyForAZoneThatHoldsNone(t *testing.T) {
	m := proxyZoneMock()
	hooks := m.proxy(t).Hooks().ClientCertificates
	ctx := context.Background()

	trusted, err := hooks.Ensure(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(trusted) != 1 || len(m.clientCertificates) != 1 || m.clientCertificates[0]["certificate"] != trusted[0] {
		t.Fatalf("Ensure = %d certificates with %v uploaded, want the one it minted and uploaded", len(trusted), m.clientCertificates)
	}
	if len(m.originPullWrites) != 0 {
		t.Fatalf("zone-level authenticated origin pulls were set %v before any origin trusted the certificate, want them left off until Present", m.originPullWrites)
	}
	if !strings.Contains(m.uploadedKeys[0], "PRIVATE KEY") {
		t.Errorf("uploaded key %q, want the certificate's private key in PEM", m.uploadedKeys[0])
	}
	leaf := parsedLeaf(t, trusted[0])
	if leaf.IsCA {
		t.Error("the minted certificate is a CA, and Cloudflare refuses anything but a leaf for zone-level authenticated origin pulls")
	}
	if len(leaf.DNSNames) == 0 {
		t.Error("the minted certificate names no SAN, and a trust config allowlists only certificates whose SAN it can check")
	}
	if lifetime := leaf.NotAfter.Sub(leaf.NotBefore); lifetime < 9*365*24*time.Hour {
		t.Errorf("the minted certificate is good for %s, want years: ocel never replaces it, and an origin that checks expiry refuses it once it lapses", lifetime)
	}

	if err := hooks.Present(ctx, "shop.app.com"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if !slices.Equal(m.originPullWrites, []bool{true}) {
		t.Errorf("zone-level authenticated origin pulls were set %v, want turned on once", m.originPullWrites)
	}

	again, err := hooks.Ensure(ctx, "www.app.com")
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if err := hooks.Present(ctx, "www.app.com"); err != nil {
		t.Fatalf("Present again: %v", err)
	}
	if !slices.Equal(again, trusted) || len(m.clientCertificates) != 1 || len(m.originPullWrites) != 1 {
		t.Errorf("a second hostname in the zone uploaded %d certificates and set pulls %v, want the one certificate the zone already holds", len(m.clientCertificates), m.originPullWrites)
	}
}

func TestTheCloudflareProxyTrustsEveryCertificateAZoneHolds(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	m.clientCertificates = []map[string]any{
		{"id": "old", "certificate": "OLD", "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
		{"id": "new", "certificate": "NEW", "status": "active", "uploaded_on": "2026-06-01T00:00:00Z"},
		{"id": "gone", "certificate": "GONE", "status": "pending_deletion", "uploaded_on": "2026-09-01T00:00:00Z"},
	}

	trusted, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !slices.Equal(trusted, []string{"NEW", "OLD"}) {
		t.Errorf("Ensure = %v, want NEW and OLD: the zone presents the latest it deployed, and during a rotation you started every origin must accept both", trusted)
	}
	if len(m.uploadedKeys) != 0 || len(m.deletedClientCertificates) != 0 {
		t.Errorf("uploaded %d and deleted %v, want nothing changed: the zone already holds certificates", len(m.uploadedKeys), m.deletedClientCertificates)
	}
}

func TestTheCloudflareProxyNeverReplacesOrDeletesAZonesClientCertificate(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	lapsed, _, err := mintClientCertificate("app.com", time.Now().Add(-clientCertificateLifetime-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m.clientCertificates = []map[string]any{{"id": "lapsed", "certificate": lapsed, "status": "active", "uploaded_on": "2016-01-01T00:00:00Z"}}
	hooks := m.proxy(t).Hooks().ClientCertificates
	ctx := context.Background()

	trusted, err := hooks.Ensure(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := hooks.Present(ctx, "shop.app.com"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if !slices.Equal(trusted, []string{lapsed}) || len(m.uploadedKeys) != 0 || len(m.deletedClientCertificates) != 0 {
		t.Errorf("Ensure = %d certificates, uploaded %d, deleted %v, want the one the zone holds and nothing changed: the zone certificate is shared by every origin it forwards to, and one project replacing it refuses the others until they deploy again", len(trusted), len(m.uploadedKeys), m.deletedClientCertificates)
	}
}

func TestTheCloudflareProxyRefusesAZoneThatPresentsCloudflaresSharedCertificate(t *testing.T) {
	for what, zone := range map[string]*cfMock{
		"global pulls on":                    {zoneID: "zone1", zoneName: "app.com", globalPulls: true},
		"zone-level pulls on with none held": {zoneID: "zone1", zoneName: "app.com", originPulls: true},
	} {
		_, err := zone.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), "shop.app.com")
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, "shared") {
			t.Errorf("%s: Ensure = %v, want an invalid refusal naming Cloudflare's shared certificate", what, err)
		}
		if len(zone.uploadedKeys) != 0 || len(zone.originPullWrites) != 0 {
			t.Errorf("%s: uploaded %d certificates and set pulls %v, want the zone untouched: another origin in it may trust only the shared certificate", what, len(zone.uploadedKeys), zone.originPullWrites)
		}
	}

	held := &cfMock{zoneID: "zone1", zoneName: "app.com", globalPulls: true, clientCertificates: []map[string]any{
		{"id": "yours", "certificate": "YOURS", "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
	}}
	err := held.proxy(t).Hooks().ClientCertificates.Present(context.Background(), "shop.app.com")
	if err == nil || len(held.originPullWrites) != 0 {
		t.Errorf("Present on a zone presenting the shared certificate that holds one of yours = %v, set pulls %v, want it refused and the zone untouched", err, held.originPullWrites)
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
