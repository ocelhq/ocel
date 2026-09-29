package cloudflare

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
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

func TestTheCloudflareProxyRefusesAPreviewWildcardItCannotServe(t *testing.T) {
	front := proxyZoneMock().proxy(t)
	spec := proxySpec()
	spec.Tier, spec.Domains = environment.TierPreview, []string{"*.preview.app.com"}

	if _, err := front.Reconcile(context.Background(), spec, edge.StackState{}); err == nil {
		t.Error("Reconcile of a preview wildcard = nil, want a refusal: previews are not forwarded through the proxy yet")
	}
	if _, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: "preview.app.com"}); err == nil {
		t.Error("ReconcilePreviewWildcard = nil, want a refusal")
	}
}

func TestTheCloudflareProxyStagesAClientCertificateForAZoneWithNoneAndUploadsItOnlyWhenPresented(t *testing.T) {
	m := proxyZoneMock()
	hooks := m.proxy(t).Hooks().ClientCertificates
	ctx := context.Background()

	staged, err := hooks.Stage(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("Stage = %d certificates, want the one it minted", len(staged))
	}
	if len(m.clientCertificates) != 0 {
		t.Fatalf("uploaded %d client certificates before any origin trusted one, want none: the zone presents what it is given at once, and an origin that does not trust it yet refuses every request", len(m.clientCertificates))
	}
	leaf := parsedLeaf(t, staged[0])
	if leaf.IsCA {
		t.Error("the staged certificate is a CA, and Cloudflare refuses anything but a leaf for zone-level authenticated origin pulls")
	}
	if len(leaf.DNSNames) == 0 {
		t.Error("the staged certificate names no SAN, and a trust config allowlists only certificates whose SAN it can check")
	}
	if lifetime := leaf.NotAfter.Sub(leaf.NotBefore); lifetime > 400*24*time.Hour {
		t.Errorf("the staged certificate is good for %s, want about a year: a leaked key stays good for as long as the certificate is", lifetime)
	}

	if err := hooks.Present(ctx, "shop.app.com"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if len(m.clientCertificates) != 1 || m.clientCertificates[0]["certificate"] != staged[0] {
		t.Fatalf("uploaded %v, want the one certificate staged", m.clientCertificates)
	}
	if !strings.Contains(m.uploadedKeys[0], "PRIVATE KEY") {
		t.Errorf("uploaded key %q, want the certificate's private key in PEM", m.uploadedKeys[0])
	}
	if !slices.Equal(m.originPullWrites, []bool{true}) {
		t.Errorf("zone-level authenticated origin pulls were set %v, want turned on once", m.originPullWrites)
	}

	again, err := hooks.Stage(ctx, "www.app.com")
	if err != nil {
		t.Fatalf("Stage again: %v", err)
	}
	if err := hooks.Present(ctx, "www.app.com"); err != nil {
		t.Fatalf("Present again: %v", err)
	}
	if !slices.Equal(again, staged) || len(m.clientCertificates) != 1 || len(m.originPullWrites) != 1 {
		t.Errorf("a second hostname in the zone uploaded %d certificates and set pulls %v, want the one certificate the zone already presents", len(m.clientCertificates), m.originPullWrites)
	}
}

func TestTheCloudflareProxyTrustsEveryCertificateAZoneMayPresent(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	m.clientCertificates = []map[string]any{
		{"id": "old", "certificate": "OLD", "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
		{"id": "new", "certificate": "NEW", "status": "active", "uploaded_on": "2026-06-01T00:00:00Z"},
		{"id": "gone", "certificate": "GONE", "status": "pending_deletion", "uploaded_on": "2026-09-01T00:00:00Z"},
	}

	staged, err := m.proxy(t).Hooks().ClientCertificates.Stage(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !slices.Equal(staged, []string{"NEW", "OLD"}) {
		t.Errorf("Stage = %v, want NEW and OLD: two runs that each uploaded one leave the zone presenting the latest, and an origin that trusts only its own refuses it", staged)
	}
	if len(m.uploadedKeys) != 0 || len(m.deletedClientCertificates) != 0 {
		t.Errorf("uploaded %d and deleted %v, want nothing changed: ocel minted neither certificate the zone presents", len(m.uploadedKeys), m.deletedClientCertificates)
	}
}

func TestTheCloudflareProxyRenewsTheClientCertificateItMintedBeforeItExpires(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	expiring, _, err := mintClientCertificate("app.com", time.Now().Add(-clientCertificateLifetime+10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m.clientCertificates = []map[string]any{{"id": "expiring", "certificate": expiring, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}}
	hooks := m.proxy(t).Hooks().ClientCertificates
	ctx := context.Background()

	staged, err := hooks.Stage(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(staged) != 2 || !slices.Contains(staged, expiring) {
		t.Fatalf("Stage = %d certificates, want the expiring one and its successor: an origin trusts both before the zone switches", len(staged))
	}
	successor := staged[slices.IndexFunc(staged, func(certificate string) bool { return certificate != expiring })]
	if err := hooks.Present(ctx, "shop.app.com"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if len(m.clientCertificates) != 2 || m.clientCertificates[1]["certificate"] != successor {
		t.Fatalf("the zone lists %v, want the successor uploaded beside the expiring certificate", m.clientCertificates)
	}

	m.clientCertificates[1]["status"] = "active"
	presented, err := hooks.Stage(ctx, "shop.app.com")
	if err != nil {
		t.Fatalf("Stage once the successor is active: %v", err)
	}
	if !slices.Equal(presented, []string{successor}) {
		t.Errorf("Stage = %d certificates once the successor is active, want the successor alone: the zone presents the latest it deployed, and an origin stops trusting the one it replaced", len(presented))
	}
	if !slices.Equal(m.deletedClientCertificates, []string{"expiring"}) {
		t.Errorf("deleted %v, want the certificate the successor replaced: Cloudflare deletes none on its own", m.deletedClientCertificates)
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
