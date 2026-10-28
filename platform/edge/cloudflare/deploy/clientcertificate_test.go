package cloudflare

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func (m *cfMock) proxyOn(t *testing.T, origin OriginScope) *Proxy {
	t.Helper()
	front := m.proxy(t)
	front.p.Scope = fixedOrigin(origin).Scope
	return front
}

func forwardThrough(t *testing.T, front *Proxy, slug, hostname string) (edge.EdgeStack, []string) {
	t.Helper()
	ctx := context.Background()
	certificates := front.Hooks().ClientCertificates
	trusted, err := certificates.Ensure(ctx, environment.TierProduction, hostname)
	if err != nil {
		t.Fatalf("Ensure(%s): %v", hostname, err)
	}
	if err := certificates.Present(ctx, environment.TierProduction, hostname); err != nil {
		t.Fatalf("Present(%s): %v", hostname, err)
	}
	stack, err := front.Reconcile(ctx, edge.StackSpec{Tier: environment.TierProduction, Slug: slug, Domains: []string{hostname}}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname, Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("BindDomain(%s): %v", hostname, err)
	}
	return stack, trusted
}

func mintOriginClientCertificate(t *testing.T, origin OriginScope, tier environment.Tier, now time.Time) (certificate, authority string) {
	t.Helper()
	certificate, _, err := mintClientCertificate(origin.String(), []string{string(tier)}, "app.com", now, clientCertificateLifetime)
	if err != nil {
		t.Fatal(err)
	}
	authority, err = readClientCA("app.com", zoneClientCertificate{ID: "held", Certificate: certificate})
	if err != nil {
		t.Fatal(err)
	}
	return certificate, authority
}

func uploadedCertificateID(t *testing.T, m *cfMock, origin OriginScope) string {
	t.Helper()
	for _, held := range m.clientCertificates {
		if read, named := (zoneClientCertificate{Certificate: fmt.Sprint(held["certificate"])}).readOwner(); named && read == (certificateOwner{origin: origin, tier: environment.TierProduction}) {
			return fmt.Sprint(held["id"])
		}
	}
	t.Fatalf("the zone holds no production client certificate of %s: %v", origin, m.clientCertificates)
	return ""
}

func TestTheClientCertificateAnOriginPresentsVerifiesAgainstTheCAEnsureReturnsAsALoadBalancerInVerifyModeChecksIt(t *testing.T) {
	m := proxyZoneMock()

	authorities, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.clientCertificates) != 1 || len(authorities) != 1 {
		t.Fatalf("Ensure = %d CAs with %d certificates uploaded, want one of each", len(authorities), len(m.clientCertificates))
	}
	if !strings.Contains(m.uploadedKeys[0], "PRIVATE KEY") {
		t.Errorf("uploaded key %q, want the certificate's private key in PEM", m.uploadedKeys[0])
	}
	presented := parsedLeaf(t, m.clientCertificates[0]["certificate"].(string))
	if presented.IsCA {
		t.Error("the certificate the origin presents is a CA, and Cloudflare presents only a leaf to an origin")
	}
	if presented.Subject.CommonName != "ocel origin pull "+testOrigin.String() {
		t.Errorf("the certificate is named %q, want it to name %s, the origin that minted it, so no other origin takes it for its own", presented.Subject.CommonName, testOrigin)
	}
	if len(presented.DNSNames) == 0 {
		t.Error("the minted certificate names no SAN, and a trust config checks the SAN of the certificate it verifies")
	}
	if lifetime := presented.NotAfter.Sub(presented.NotBefore); lifetime < 9*365*24*time.Hour {
		t.Errorf("the minted certificate is good for %s, want years: ocel never replaces it, and an origin that checks expiry refuses it once it lapses", lifetime)
	}
	roots := x509.NewCertPool()
	for _, authority := range authorities {
		ca := parsedLeaf(t, authority)
		if !ca.BasicConstraintsValid || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
			t.Errorf("Ensure returned %q, which is no CA that signs certificates, and a load balancer's trust store refuses a bundle holding one", ca.Subject)
		}
		roots.AddCert(ca)
	}
	if _, err := presented.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("the certificate the origin presents does not verify as a client against the CAs Ensure returned: %v", err)
	}
}

func TestTwoOriginsForwardingThroughOneZoneEachTrustOnlyTheCertificateItMinted(t *testing.T) {
	m := proxyZoneMock()
	ours, theirs := m.proxy(t), m.proxyOn(t, otherOrigin)

	_, ourCAs := forwardThrough(t, ours, "acme", "shop.app.com")
	_, theirCAs := forwardThrough(t, theirs, "acme", "blog.app.com")

	if len(m.clientCertificates) != 2 || len(ourCAs) != 1 || len(theirCAs) != 1 || ourCAs[0] == theirCAs[0] {
		t.Fatalf("the zone holds %d certificates and the origins trust %d and %d CAs, want one certificate and one CA of its own each", len(m.clientCertificates), len(ourCAs), len(theirCAs))
	}
	ourID, theirID := uploadedCertificateID(t, m, testOrigin), uploadedCertificateID(t, m, otherOrigin)
	roots := x509.NewCertPool()
	roots.AddCert(parsedLeaf(t, ourCAs[0]))
	for _, held := range m.clientCertificates {
		_, err := parsedLeaf(t, fmt.Sprint(held["certificate"])).Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		if mine := held["id"] == ourID; mine != (err == nil) {
			t.Errorf("certificate %s verifies against this origin's CA: %v, want only its own to", held["id"], err == nil)
		}
	}
	for hostname, want := range map[string]string{"shop.app.com": ourID, "blog.app.com": theirID} {
		if association := m.associations[hostname]; association["cert_id"] != want || association["enabled"] != true {
			t.Errorf("%s is associated with %v, want certificate %s enabled: Cloudflare presents to each origin only the certificate that origin trusts", hostname, association, want)
		}
	}
}

func TestAnOriginNeverTakesACertificateAnotherOriginOrAPersonUploadedForItsOwn(t *testing.T) {
	m := proxyZoneMock()
	theirs, theirCA := mintOriginClientCertificate(t, otherOrigin, environment.TierProduction, time.Now())
	m.clientCertificates = []map[string]any{
		{"id": "theirs", "certificate": theirs, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
		{"id": "yours", "certificate": mintSelfSignedClientCertificate(t), "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"},
	}

	trusted, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.uploadedKeys) != 1 || len(trusted) != 1 || trusted[0] == theirCA {
		t.Errorf("Ensure = %d CAs after uploading %d certificates, want only the CA of one it minted: %s's certificate and yours are not this origin's to present", len(trusted), len(m.uploadedKeys), otherOrigin)
	}
	if len(m.deletedClientCertificates) != 0 {
		t.Errorf("deleted %v, want every certificate another origin or a person uploaded left in place", m.deletedClientCertificates)
	}
}

func TestEveryHostnameAnOriginForwardsIsAssociatedWithTheOneCertificateItMinted(t *testing.T) {
	m := proxyZoneMock()
	front := m.proxy(t)

	_, first := forwardThrough(t, front, "acme", "shop.app.com")
	_, second := forwardThrough(t, front, "acme", "www.app.com")

	if len(m.clientCertificates) != 1 || !slices.Equal(first, second) {
		t.Fatalf("the zone holds %d certificates and the hostnames trust %d and %d CAs, want one certificate both trust", len(m.clientCertificates), len(first), len(second))
	}
	id := uploadedCertificateID(t, m, testOrigin)
	for _, hostname := range []string{"shop.app.com", "www.app.com"} {
		if association := m.associations[hostname]; association["cert_id"] != id || association["enabled"] != true {
			t.Errorf("%s is associated with %v, want certificate %s enabled", hostname, association, id)
		}
	}

	if err := front.Hooks().ClientCertificates.Present(context.Background(), environment.TierProduction, "shop.app.com"); err != nil {
		t.Fatalf("Present again: %v", err)
	}
	if len(m.associationWrites) != 2 {
		t.Errorf("associated %v, want each hostname written once: one already presenting this origin's certificate is left as it is", m.associationWrites)
	}
}

func TestAnOriginRefusesAHostnameCloudflarePresentsAnotherOriginsCertificateFor(t *testing.T) {
	m := proxyZoneMock()
	theirs, _ := mintOriginClientCertificate(t, otherOrigin, environment.TierProduction, time.Now())
	m.clientCertificates = []map[string]any{{"id": "theirs", "certificate": theirs, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}}
	m.associations = map[string]map[string]any{"shop.app.com": {"hostname": "shop.app.com", "cert_id": "theirs", "certificate": theirs, "enabled": true, "cert_status": "active", "status": "active"}}
	certificates := m.proxy(t).Hooks().ClientCertificates

	for call, err := range map[string]error{
		"RefuseUnready": certificates.RefuseUnready(context.Background(), environment.TierProduction, "shop.app.com"),
		"Present":       certificates.Present(context.Background(), environment.TierProduction, "shop.app.com"),
	} {
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, otherOrigin.String()) {
			t.Errorf("%s = %v, want an invalid refusal naming %s, whose origin trusts only the certificate presented for it", call, err, otherOrigin)
		}
	}
	if len(m.associationWrites) != 0 {
		t.Errorf("associated %v, want shop.app.com left presenting %s's certificate", m.associationWrites, otherOrigin)
	}
}

func TestReleasingAHostnameDropsOnlyItsAssociationAndKeepsTheOriginsCertificate(t *testing.T) {
	m := proxyZoneMock()
	front := m.proxy(t)
	shop, _ := forwardThrough(t, front, "acme", "shop.app.com")
	www, _ := forwardThrough(t, front, "web", "www.app.com")
	id := uploadedCertificateID(t, m, testOrigin)

	if err := shop.UnbindDomain(context.Background(), "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain(shop.app.com): %v", err)
	}
	if _, held := m.associations["shop.app.com"]; held {
		t.Errorf("shop.app.com is still associated with %v, want the association dropped with the hostname", m.associations["shop.app.com"])
	}
	if association := m.associations["www.app.com"]; association["cert_id"] != id {
		t.Errorf("www.app.com is associated with %v, want it left on certificate %s", association, id)
	}

	if err := www.UnbindDomain(context.Background(), "www.app.com"); err != nil {
		t.Fatalf("UnbindDomain(www.app.com): %v", err)
	}
	if len(m.associations) != 0 || len(m.deletedClientCertificates) != 0 || len(m.clientCertificates) != 1 {
		t.Errorf("associations %v left, deleted %v, want every association dropped and certificate %s kept: it lives as long as the origin's bootstrap", m.associations, m.deletedClientCertificates, id)
	}
}

func TestADeployAfterAReleaseFindsTheCertificateItsOriginAlreadyTrusts(t *testing.T) {
	m := proxyZoneMock()
	front := m.proxy(t)
	shop, before := forwardThrough(t, front, "acme", "shop.app.com")
	if err := shop.UnbindDomain(context.Background(), "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain(shop.app.com): %v", err)
	}

	_, after := forwardThrough(t, front, "web", "www.app.com")

	if !slices.Equal(before, after) || len(m.uploadedKeys) != 1 {
		t.Errorf("a deploy after the release trusts %d CAs, the same as before: %v, after %d uploads, want the one certificate the origin already presents", len(after), slices.Equal(before, after), len(m.uploadedKeys))
	}
	if association := m.associations["www.app.com"]; association["cert_id"] != uploadedCertificateID(t, m, testOrigin) {
		t.Errorf("www.app.com is associated with %v, want the origin's certificate", association)
	}
}

func TestReleasingAHostnameAnotherProjectOfTheOriginForwardsLeavesItsAssociation(t *testing.T) {
	m := proxyZoneMock()
	front := m.proxy(t)
	forwardThrough(t, front, "acme", "shop.app.com")
	other, _ := forwardThrough(t, front, "web", "www.app.com")

	if err := other.UnbindDomain(context.Background(), "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain(shop.app.com) from project web: %v", err)
	}
	if association := m.associations["shop.app.com"]; association["cert_id"] != uploadedCertificateID(t, m, testOrigin) {
		t.Errorf("shop.app.com is associated with %v, want it left: project acme still forwards it", association)
	}
}

func TestReleasingAHostnameLeavesAnotherOriginsAssociationAndCertificate(t *testing.T) {
	m := proxyZoneMock()
	ours, theirs := m.proxy(t), m.proxyOn(t, otherOrigin)
	shop, _ := forwardThrough(t, ours, "acme", "shop.app.com")
	forwardThrough(t, theirs, "acme", "blog.app.com")
	theirID := uploadedCertificateID(t, m, otherOrigin)

	for _, hostname := range []string{"blog.app.com", "shop.app.com"} {
		if err := shop.UnbindDomain(context.Background(), hostname); err != nil {
			t.Fatalf("UnbindDomain(%s): %v", hostname, err)
		}
	}
	if association := m.associations["blog.app.com"]; association["cert_id"] != theirID {
		t.Errorf("blog.app.com is associated with %v, want it left on %s's certificate", association, otherOrigin)
	}
	if len(m.deletedClientCertificates) != 0 {
		t.Errorf("deleted %v, want every certificate kept", m.deletedClientCertificates)
	}
}

func TestDestroyingAPreviewWildcardDropsItsAssociationAndKeepsTheCertificate(t *testing.T) {
	m := proxyZoneMock()
	m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
	front := m.proxy(t)
	ctx := context.Background()
	certificates := front.Hooks().ClientCertificates
	if _, err := certificates.Ensure(ctx, environment.TierPreview, "*.preview.app.com"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := certificates.Present(ctx, environment.TierPreview, "*.preview.app.com"); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if _, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: &edge.Origin{Address: "198.51.100.4"}}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}

	if err := front.DestroyPreviewWildcard(ctx, "preview.app.com"); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if len(m.associations) != 0 || len(m.deletedClientCertificates) != 0 {
		t.Errorf("associations %v left and deleted %v, want the wildcard's association dropped and the certificate kept", m.associations, m.deletedClientCertificates)
	}
}

func TestEachTierOfAnOriginPresentsACertificateOfItsOwn(t *testing.T) {
	m := proxyZoneMock()
	certificates := m.proxy(t).Hooks().ClientCertificates

	production, err := certificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure(production): %v", err)
	}
	preview, err := certificates.Ensure(context.Background(), environment.TierPreview, "*.preview.app.com")
	if err != nil {
		t.Fatalf("Ensure(preview): %v", err)
	}
	if len(m.clientCertificates) != 2 || slices.Equal(production, preview) {
		t.Errorf("the zone holds %d certificates and the tiers trust the same CA: %v, want one per tier, so tearing one tier down leaves the other's", len(m.clientCertificates), slices.Equal(production, preview))
	}
}

func tierTeardowns() map[string]func(*testing.T, *cfMock) error {
	return map[string]func(*testing.T, *cfMock) error{
		"the Cloudflare proxy": func(t *testing.T, m *cfMock) error {
			return m.proxy(t).Teardown(context.Background(), environment.TierProduction)
		},
		"the Cloudflare edge": func(t *testing.T, m *cfMock) error {
			t.Setenv(envAccountID, "acct")
			return m.provider(t).Teardown(context.Background(), environment.TierProduction)
		},
	}
}

func heldClientCertificate(id, certificate string) map[string]any {
	return map[string]any{"id": id, "certificate": certificate, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}
}

func recordZones(t *testing.T, m *cfMock, tier environment.Tier, zones ...recordedZone) {
	t.Helper()
	writeTierRecord(t, m.store(), tier, func(recorded *tierRecord) { recorded.ClientCertificateZones = zones })
}

func readRecordedZones(t *testing.T, m *cfMock, tier environment.Tier) []recordedZone {
	t.Helper()
	return readTierRecordOf(t, m.store(), tier).ClientCertificateZones
}

func TestMintingATiersCertificateInAZoneRecordsThatZoneForTheOrigin(t *testing.T) {
	m := proxyZoneMock()
	certificates := m.proxy(t).Hooks().ClientCertificates

	for range 2 {
		if _, err := certificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com"); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}

	if got, want := readRecordedZones(t, m, environment.TierProduction), []recordedZone{{ID: "zone1", Name: "app.com"}}; !slices.Equal(got, want) {
		t.Errorf("the production tier records zones %v, want %v: teardown reaches only the zones recorded", got, want)
	}
	if got := readRecordedZones(t, m, environment.TierPreview); len(got) != 0 {
		t.Errorf("the preview tier records zones %v, want none: it minted nothing", got)
	}
}

func TestTearingATierDownDeletesThisOriginsCertificatesInTheZonesItRecordedAndListsNoOthers(t *testing.T) {
	ours, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	oursElsewhere, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	ourPreview, _ := mintOriginClientCertificate(t, testOrigin, environment.TierPreview, time.Now())
	theirs, _ := mintOriginClientCertificate(t, otherOrigin, environment.TierProduction, time.Now())
	for front, teardown := range tierTeardowns() {
		m := proxyZoneMock()
		m.clientCertificates = []map[string]any{heldClientCertificate("ours", ours), heldClientCertificate("our-preview", ourPreview), heldClientCertificate("theirs", theirs), heldClientCertificate("yours", mintSelfSignedClientCertificate(t))}
		m.extraZones = []cfZone{
			{id: "zone2", name: "shop.dev", certificates: []map[string]any{heldClientCertificate("ours-elsewhere", oursElsewhere), heldClientCertificate("theirs-elsewhere", theirs)}},
			{id: "zone3", name: "unrecorded.dev", certificates: []map[string]any{heldClientCertificate("ours-unrecorded", ours)}},
		}
		recordZones(t, m, environment.TierProduction, recordedZone{ID: "zone1", Name: "app.com"}, recordedZone{ID: "zone2", Name: "shop.dev"})

		if err := teardown(t, m); err != nil {
			t.Fatalf("%s: Teardown: %v", front, err)
		}
		if want := []string{"ours", "ours-elsewhere"}; !slices.Equal(slices.Sorted(slices.Values(m.deletedClientCertificates)), want) {
			t.Errorf("%s: Teardown deleted %v, want only this origin's production certificates %v in the zones it recorded", front, m.deletedClientCertificates, want)
		}
		if m.accountZoneLists != 0 {
			t.Errorf("%s: Teardown listed every zone in the account %d times, want only the recorded zones reached", front, m.accountZoneLists)
		}
		if got := readRecordedZones(t, m, environment.TierProduction); len(got) != 0 {
			t.Errorf("%s: the production tier still records zones %v after teardown, want the record dropped", front, got)
		}
	}
}

func TestTearingDownATierThatRecordedNoZoneDeletesNothing(t *testing.T) {
	ours, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	for front, teardown := range tierTeardowns() {
		m := proxyZoneMock()
		m.clientCertificates = []map[string]any{heldClientCertificate("ours", ours)}

		if err := teardown(t, m); err != nil {
			t.Fatalf("%s: Teardown: %v", front, err)
		}
		if len(m.deletedClientCertificates) != 0 || m.accountZoneLists != 0 {
			t.Errorf("%s: Teardown deleted %v and listed every zone %d times, want nothing reached: the tier recorded no zone", front, m.deletedClientCertificates, m.accountZoneLists)
		}
	}
}

func TestPlanningATiersRemovalNamesItsCertificatesInTheZonesItRecorded(t *testing.T) {
	ours, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	m := proxyZoneMock()
	m.clientCertificates = []map[string]any{heldClientCertificate("ours", ours)}
	recordZones(t, m, environment.TierProduction, recordedZone{ID: "zone1", Name: "app.com"})

	planned, err := m.proxy(t).Hooks().PlanRemoveBootstrap(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap: %v", err)
	}
	if len(planned) != 1 || planned[0].Name != "app.com/ours" || planned[0].Action != edge.PlanDelete || m.accountZoneLists != 0 {
		t.Errorf("PlanRemoveBootstrap = %+v after listing every zone %d times, want certificate ours in app.com deleted and no zone listed", planned, m.accountZoneLists)
	}
}

func TestOfTwoRunsOfOneOriginThatUploadACertificateAtOnceTheSecondDeletesItsOwn(t *testing.T) {
	m := proxyZoneMock()
	otherRun, otherRunCA := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	m.afterListing = func(m *cfMock) {
		m.clientCertificates = append(m.clientCertificates, map[string]any{"id": "other-run", "certificate": otherRun, "status": "pending_deployment", "uploaded_on": "2026-09-29T09:00:00Z"})
	}

	trusted, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !slices.Equal(trusted, []string{otherRunCA}) {
		t.Errorf("Ensure = %v, want only the certificate the other run uploaded first", trusted)
	}
	if len(m.clientCertificates) != 1 || m.clientCertificates[0]["id"] != "other-run" {
		t.Errorf("the zone holds %v, want the other run's certificate alone: an origin presents one", m.clientCertificates)
	}
}

func TestOfTwoRunsOfOneOriginThatUploadACertificateAtOnceTheFirstKeepsItsOwn(t *testing.T) {
	m := proxyZoneMock()
	otherRun, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	m.afterListing = func(m *cfMock) {
		m.clientCertificates = append(m.clientCertificates, map[string]any{"id": "other-run", "certificate": otherRun, "status": "pending_deployment", "uploaded_on": "2026-09-29T11:00:00Z"})
	}

	trusted, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.deletedClientCertificates) != 0 || len(trusted) != 1 {
		t.Errorf("Ensure = %d CAs, deleted %v, want the certificate uploaded first kept and trusted alone: the run that uploaded after it deletes its own", len(trusted), m.deletedClientCertificates)
	}
}

func TestAnotherOriginsCertificateUploadedAtOnceLeavesTheOneThisOriginUploaded(t *testing.T) {
	m := proxyZoneMock()
	theirs, theirCA := mintOriginClientCertificate(t, otherOrigin, environment.TierProduction, time.Now())
	m.afterListing = func(m *cfMock) {
		m.clientCertificates = append(m.clientCertificates, map[string]any{"id": "theirs", "certificate": theirs, "status": "pending_deployment", "uploaded_on": "2026-09-29T09:00:00Z"})
	}

	trusted, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.deletedClientCertificates) != 0 || len(trusted) != 1 || trusted[0] == theirCA {
		t.Errorf("Ensure = %d CAs, deleted %v, want this origin's certificate kept and trusted alone", len(trusted), m.deletedClientCertificates)
	}
}

func TestAZoneHoldingAsManyClientCertificatesAsCloudflareAllowsNoneOfThemThisOriginsIsRefusedBeforeAnythingIsForwarded(t *testing.T) {
	m := proxyZoneMock()
	for i := range maxZoneClientCertificates {
		m.clientCertificates = append(m.clientCertificates, map[string]any{"id": fmt.Sprintf("held-%d", i), "certificate": mintSelfSignedClientCertificate(t), "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"})
	}
	certificates := m.proxy(t).Hooks().ClientCertificates

	for call, err := range map[string]error{
		"RefuseUnready": certificates.RefuseUnready(context.Background(), environment.TierProduction, "shop.app.com"),
		"Ensure": func() error {
			_, err := certificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
			return err
		}(),
	} {
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, fmt.Sprint(maxZoneClientCertificates)) {
			t.Errorf("%s = %v, want an invalid refusal naming Cloudflare's limit of %d", call, err, maxZoneClientCertificates)
		}
	}
	if len(m.uploadedKeys) != 0 {
		t.Errorf("uploaded %d certificates, want none past the limit", len(m.uploadedKeys))
	}
}

func TestATokenThatCannotUploadTheOriginsClientCertificateIsRefusedNamingThePermissionItLacks(t *testing.T) {
	m := proxyZoneMock()
	m.deniedClientCertificates.writes = true

	_, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	assertRefusedForSSLAndCertificatesEdit(t, "Ensure on a zone holding no certificate of this origin", err)
}

func TestATokenThatCannotAssociateAHostnameWithTheOriginsCertificateIsRefusedNamingThePermissionItLacks(t *testing.T) {
	m := proxyZoneMock()
	held, _ := mintOriginClientCertificate(t, testOrigin, environment.TierProduction, time.Now())
	m.clientCertificates = []map[string]any{{"id": "held", "certificate": held, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}}
	m.deniedClientCertificates.writes = true

	err := m.proxy(t).Hooks().ClientCertificates.Present(context.Background(), environment.TierProduction, "shop.app.com")
	assertRefusedForSSLAndCertificatesEdit(t, "Present on a hostname associated with no certificate", err)
}

func TestATokenThatCannotReadTheZonesClientCertificatesIsRefusedNamingThePermissionItLacks(t *testing.T) {
	m := proxyZoneMock()
	m.deniedClientCertificates.reads = true

	_, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), environment.TierProduction, "shop.app.com")
	assertRefusedForSSLAndCertificatesEdit(t, "Ensure with the zone's client certificates unreadable", err)
}

func clientCertificateFronts(t *testing.T, m *cfMock) map[string]*edge.ClientCertificateHooks {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	return map[string]*edge.ClientCertificateHooks{
		"the Cloudflare edge":  m.provider(t).Hooks().ClientCertificates,
		"the Cloudflare proxy": m.proxy(t).Hooks().ClientCertificates,
	}
}

func TestAZoneThatDoesNotValidateTheOriginsCertificateIsRefusedBeforeAnythingIsForwardedThroughIt(t *testing.T) {
	for _, mode := range []string{"off", "flexible", "full"} {
		m := proxyZoneMock()
		m.sslMode = mode
		for front, certificates := range clientCertificateFronts(t, m) {
			err := certificates.RefuseUnready(context.Background(), environment.TierProduction, "shop.app.com")

			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", mode)) || !strings.Contains(err.Error(), "Full (strict)") {
				t.Errorf("%s: RefuseUnready in a zone whose SSL mode is %s = %v, want it refused naming the mode and Full (strict), as binding the hostname would be", front, mode, err)
			}
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Errorf("%s: RefuseUnready in a zone whose SSL mode is %s = %v, want an invalid refusal: the zone's setting is the user's to change", front, mode, err)
			}
		}
	}
}

func TestATokenThatCannotReadTheZonesClientCertificatesIsRefusedBeforeAnythingIsForwarded(t *testing.T) {
	m := proxyZoneMock()
	m.deniedClientCertificates.reads = true

	for front, certificates := range clientCertificateFronts(t, m) {
		assertRefusedForSSLAndCertificatesEdit(t, front+": RefuseUnready with the zone's client certificates unreadable", certificates.RefuseUnready(context.Background(), environment.TierProduction, "shop.app.com"))
	}
}

func TestAStrictZoneWhoseClientCertificatesTheTokenReadsIsReadyAndLeftUnchanged(t *testing.T) {
	for _, mode := range []string{"strict", "origin_pull"} {
		m := proxyZoneMock()
		m.sslMode = mode
		for front, certificates := range clientCertificateFronts(t, m) {
			if err := certificates.RefuseUnready(context.Background(), environment.TierProduction, "shop.app.com"); err != nil {
				t.Errorf("%s: RefuseUnready in a zone whose SSL mode is %s = %v, want it ready", front, mode, err)
			}
		}
		if len(m.uploadedKeys) != 0 || len(m.associationWrites) != 0 {
			t.Errorf("SSL mode %s: RefuseUnready uploaded %d certificates and associated %v, want nothing written: a check runs before the deploy is consented to", mode, len(m.uploadedKeys), m.associationWrites)
		}
	}
}

func assertRefusedForSSLAndCertificatesEdit(t *testing.T, call string, err error) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("%s answered 403 = %v, want a denied refusal: the token lacks a permission ocel lists, which the user fixes, not an internal error", call, err)
	}
	for _, named := range []string{"Zone · SSL and Certificates · Edit", "app.com", apiTokensPage, setupDocs} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("%s refused with %q, want it to name %q", call, refused.Message, named)
		}
	}
}

func mintSelfSignedClientCertificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "your own"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestARefusedClientCertificateUploadIsTracedWithoutItsPrivateKey(t *testing.T) {
	m := proxyZoneMock()
	m.deniedClientCertificates.writes = true
	certificates := m.proxy(t).Hooks().ClientCertificates
	ctx, spans := tracingtest.Record(t)

	if _, err := certificates.Ensure(ctx, environment.TierProduction, "shop.app.com"); err == nil {
		t.Fatal("Ensure = nil, want the refusal")
	}
	assertRefusedWithoutContent(t, spans, "PRIVATE KEY")
}
