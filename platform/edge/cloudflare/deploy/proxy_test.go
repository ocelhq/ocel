package cloudflare

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"testing"

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

func TestTheCloudflareProxyUploadsOneClientCertificateAZoneWithNoneOfItsOwnPresents(t *testing.T) {
	m := proxyZoneMock()
	front := m.proxy(t)

	presented, err := front.Hooks().EnsureClientCertificate(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("EnsureClientCertificate: %v", err)
	}
	if len(m.clientCertificates) != 1 {
		t.Fatalf("uploaded %d client certificates, want one", len(m.clientCertificates))
	}
	if presented != m.clientCertificates[0]["certificate"] {
		t.Errorf("EnsureClientCertificate named another certificate than the one uploaded")
	}
	block, _ := pem.Decode([]byte(presented))
	if block == nil {
		t.Fatalf("the uploaded certificate %q is no PEM", presented)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the uploaded certificate: %v", err)
	}
	if leaf.IsCA {
		t.Error("the uploaded certificate is a CA, and Cloudflare refuses anything but a leaf for zone-level authenticated origin pulls")
	}
	if len(leaf.DNSNames) == 0 {
		t.Error("the uploaded certificate names no SAN, and a trust config allowlists only certificates whose SAN it can check")
	}
	if !strings.Contains(m.uploadedKeys[0], "PRIVATE KEY") {
		t.Errorf("uploaded key %q, want the certificate's private key in PEM", m.uploadedKeys[0])
	}
	if !slices.Equal(m.originPullWrites, []bool{true}) {
		t.Errorf("zone-level authenticated origin pulls were set %v, want turned on once", m.originPullWrites)
	}

	again, err := front.Hooks().EnsureClientCertificate(context.Background(), "www.app.com")
	if err != nil {
		t.Fatalf("EnsureClientCertificate again: %v", err)
	}
	if again != presented || len(m.clientCertificates) != 1 || len(m.originPullWrites) != 1 {
		t.Errorf("a second hostname in the zone uploaded %d certificates and set pulls %v, want the one certificate the zone already presents", len(m.clientCertificates), m.originPullWrites)
	}
}

func TestTheCloudflareProxyPresentsTheLatestCertificateAZoneAlreadyHas(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	m.clientCertificates = []map[string]any{
		{"id": "old", "certificate": "OLD", "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
		{"id": "new", "certificate": "NEW", "status": "active", "uploaded_on": "2026-06-01T00:00:00Z"},
		{"id": "gone", "certificate": "GONE", "status": "pending_deletion", "uploaded_on": "2026-09-01T00:00:00Z"},
	}

	presented, err := m.proxy(t).Hooks().EnsureClientCertificate(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("EnsureClientCertificate: %v", err)
	}
	if presented != "NEW" {
		t.Errorf("EnsureClientCertificate = %q, want NEW, the latest the zone presents", presented)
	}
	if len(m.uploadedKeys) != 0 || len(m.originPullWrites) != 0 {
		t.Errorf("uploaded %d certificates and set pulls %v, want nothing changed on a zone already presenting one", len(m.uploadedKeys), m.originPullWrites)
	}
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
