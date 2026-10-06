package cloudflare

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v4/r2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const productionCertificateBase = "ocel-origin-client"

func parseCertificate(t *testing.T, certificatePEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil {
		t.Fatalf("no PEM block in %q", certificatePEM)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return certificate
}

func (m *cfMock) holdWorkerClientCertificate(t *testing.T, name string, expires time.Time) string {
	t.Helper()
	leaf, _, err := mintClientCertificate(productionCertificateBase, expires.Add(-clientCertificateLifetime))
	if err != nil {
		t.Fatalf("mint a client certificate: %v", err)
	}
	id := fmt.Sprintf("held-%d", len(m.mtlsCertificates)+1)
	m.mtlsCertificates = append(m.mtlsCertificates, map[string]any{
		"id": id, "ca": false, "certificates": leaf, "name": name,
		"expires_on": certificateNotAfter(leaf).Format(time.RFC3339),
	})
	return id
}

func certificateName(expires time.Time) string {
	return productionCertificateBase + "-" + expires.UTC().Format("20060102")
}

func mutualTLSEdge(t *testing.T, m *cfMock) *cloudflare {
	t.Helper()
	seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
	p := m.provider(t)
	p.workerClientCertificate = true
	p.objects = func(string, r2.TemporaryCredentialNewResponse) objectAPI { return &fakeObjects{} }
	return p
}

func workerCertificateOffer(t *testing.T, out edge.BootstrapOutput) edge.Offer {
	t.Helper()
	var found []edge.Offer
	for _, offer := range out.Offers {
		if offer.Kind == edge.OfferWorkerClientCertificate {
			found = append(found, offer)
		}
	}
	if len(found) != 1 {
		t.Fatalf("offers = %+v, want exactly one %q", out.Offers, edge.OfferWorkerClientCertificate)
	}
	return found[0]
}

func authoritiesOf(t *testing.T, offer edge.Offer) []*x509.Certificate {
	t.Helper()
	rest := []byte(offer.Values[edge.OfferKeyClientCertificateAuthorities])
	var authorities []*x509.Certificate
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return authorities
		}
		authority, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse an offered CA: %v", err)
		}
		authorities = append(authorities, authority)
	}
}

func TestABootstrapUploadsOneClientCertificateForItsWorkersAndOffersItsCA(t *testing.T) {
	m := bootstrapMock(t, true)
	p := mutualTLSEdge(t, m)

	before := time.Now()
	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(m.uploadedMTLSCertificates) != 1 {
		t.Fatalf("uploads = %v, want one", m.uploadedMTLSCertificates)
	}
	upload := m.uploadedMTLSCertificates[0]
	if upload["ca"] != false {
		t.Errorf("ca = %v, want false: the worker presents a leaf", upload["ca"])
	}
	if upload["private_key"] == nil || upload["private_key"] == "" {
		t.Error("the upload carries no private key, so a worker could not present the certificate")
	}
	leaf := parseCertificate(t, fmt.Sprint(upload["certificates"]))
	if leaf.IsCA || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Errorf("uploaded leaf: IsCA = %v, ExtKeyUsage = %v, want a clientAuth leaf", leaf.IsCA, leaf.ExtKeyUsage)
	}
	if want := certificateName(before.Add(clientCertificateLifetime)); upload["name"] != want {
		t.Errorf("name = %v, want %q", upload["name"], want)
	}

	offer := workerCertificateOffer(t, out)
	if offer.Values[edge.OfferKeyClientCertificateID] != "mtls-1" {
		t.Errorf("certificateId = %q, want the uploaded certificate's id", offer.Values[edge.OfferKeyClientCertificateID])
	}
	authorities := authoritiesOf(t, offer)
	if len(authorities) != 1 {
		t.Fatalf("offered %d CAs, want one", len(authorities))
	}
	if err := leaf.CheckSignatureFrom(authorities[0]); err != nil || !authorities[0].IsCA {
		t.Errorf("the offered CA does not verify the uploaded leaf: %v (IsCA = %v)", err, authorities[0].IsCA)
	}
}

func TestABootstrapKeepsAClientCertificateFarFromExpiry(t *testing.T) {
	m := bootstrapMock(t, true)
	expires := time.Now().Add(5 * 365 * 24 * time.Hour)
	id := m.holdWorkerClientCertificate(t, certificateName(expires), expires)
	p := mutualTLSEdge(t, m)

	first, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	second, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}

	if len(m.uploadedMTLSCertificates) != 0 || len(m.deletedMTLSCertificates) != 0 {
		t.Errorf("uploads = %v, deletes = %v, want none for a certificate far from expiry", m.uploadedMTLSCertificates, m.deletedMTLSCertificates)
	}
	a, b := workerCertificateOffer(t, first), workerCertificateOffer(t, second)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("offers differ between bootstraps: %+v vs %+v", a, b)
	}
	if a.Values[edge.OfferKeyClientCertificateID] != id {
		t.Errorf("certificateId = %q, want the held %q", a.Values[edge.OfferKeyClientCertificateID], id)
	}
}

func TestABootstrapUploadsANewClientCertificateBesideOneNearExpiryAndTrustsBoth(t *testing.T) {
	m := bootstrapMock(t, true)
	expires := time.Now().Add(100 * 24 * time.Hour)
	old := m.holdWorkerClientCertificate(t, certificateName(expires), expires)
	p := mutualTLSEdge(t, m)

	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(m.uploadedMTLSCertificates) != 1 || len(m.deletedMTLSCertificates) != 0 {
		t.Fatalf("uploads = %d, deletes = %v, want one upload and the old certificate kept", len(m.uploadedMTLSCertificates), m.deletedMTLSCertificates)
	}
	offer := workerCertificateOffer(t, out)
	if got := offer.Values[edge.OfferKeyClientCertificateID]; got != "mtls-1" || got == old {
		t.Errorf("certificateId = %q, want the new certificate's id", got)
	}
	authorities := authoritiesOf(t, offer)
	if len(authorities) != 2 {
		t.Fatalf("offered %d CAs, want the new one and the old one", len(authorities))
	}
	newLeaf := parseCertificate(t, fmt.Sprint(m.uploadedMTLSCertificates[0]["certificates"]))
	oldLeaf := parseCertificate(t, fmt.Sprint(m.mtlsCertificates[0]["certificates"]))
	if newLeaf.CheckSignatureFrom(authorities[0]) != nil {
		t.Error("the first offered CA is not the new certificate's")
	}
	if oldLeaf.CheckSignatureFrom(authorities[1]) != nil {
		t.Error("the second offered CA is not the old certificate's")
	}
}

func TestABootstrapDeletesAnExpiredClientCertificateAndStopsOfferingItsCA(t *testing.T) {
	m := bootstrapMock(t, true)
	current := time.Now().Add(5 * 365 * 24 * time.Hour)
	expired := time.Now().Add(-24 * time.Hour)
	m.holdWorkerClientCertificate(t, certificateName(expired), expired)
	keptID := m.holdWorkerClientCertificate(t, certificateName(current), current)
	p := mutualTLSEdge(t, m)

	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if !reflect.DeepEqual(m.deletedMTLSCertificates, []string{"held-1"}) {
		t.Errorf("deleted = %v, want the expired certificate alone", m.deletedMTLSCertificates)
	}
	if len(m.uploadedMTLSCertificates) != 0 {
		t.Errorf("uploads = %v, want none", m.uploadedMTLSCertificates)
	}
	offer := workerCertificateOffer(t, out)
	if offer.Values[edge.OfferKeyClientCertificateID] != keptID {
		t.Errorf("certificateId = %q, want %q", offer.Values[edge.OfferKeyClientCertificateID], keptID)
	}
	if got := len(authoritiesOf(t, offer)); got != 1 {
		t.Errorf("offered %d CAs, want only the current one", got)
	}
}

func TestABootstrapThatCannotDeleteAnExpiredClientCertificateStillSucceeds(t *testing.T) {
	m := bootstrapMock(t, true)
	m.refuseMTLSDelete = true
	current := time.Now().Add(5 * 365 * 24 * time.Hour)
	expired := time.Now().Add(-24 * time.Hour)
	m.holdWorkerClientCertificate(t, certificateName(expired), expired)
	m.holdWorkerClientCertificate(t, certificateName(current), current)
	p := mutualTLSEdge(t, m)

	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v, want it to succeed though Cloudflare refuses the delete", err)
	}
	if got := len(authoritiesOf(t, workerCertificateOffer(t, out))); got != 1 {
		t.Errorf("offered %d CAs, want only the current one", got)
	}

	changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("planBootstrap: %v", err)
	}
	want := edge.PlanChange{Kind: kindMTLSCertificate, Name: certificateName(expired), Action: edge.PlanDelete, Reason: "expired"}
	if !slices.Contains(changes, want) {
		t.Errorf("plan = %+v, want it to still list %+v", changes, want)
	}
	parts, err := p.describeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("describeBootstrap: %v", err)
	}
	for _, part := range parts {
		if part.Name == certificateName(expired) && part.Current {
			t.Errorf("the undeleted expired certificate shows as current: %+v", part)
		}
	}
}

func TestABootstrapRefusesAHeldClientCertificateWhoseCAItCannotRead(t *testing.T) {
	m := bootstrapMock(t, true)
	expires := time.Now().Add(5 * 365 * 24 * time.Hour)
	m.mtlsCertificates = append(m.mtlsCertificates, map[string]any{
		"id": "unreadable-1", "ca": false, "certificates": "", "name": certificateName(expires),
		"expires_on": expires.Format(time.RFC3339),
	})
	p := mutualTLSEdge(t, m)

	_, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err == nil {
		t.Fatal("Bootstrap err = nil, want a refusal")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("err = %v, want a refusal with code %q", err, refusal.CodeInvalid)
	}
	for _, want := range []string{"unreadable-1", "wrangler mtls-certificate delete --id unreadable-1", "ocel bootstrap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to say %q", err, want)
		}
	}
	if len(m.uploadedMTLSCertificates) != 0 {
		t.Errorf("uploads = %v, want none: an unreadable certificate is never replaced silently", m.uploadedMTLSCertificates)
	}
}

func TestTheProductionClientCertificateIgnoresThePreviewTiersCertificates(t *testing.T) {
	m := bootstrapMock(t, true)
	expires := time.Now().Add(5 * 365 * 24 * time.Hour)
	m.holdWorkerClientCertificate(t, "ocel-origin-client-preview-"+expires.UTC().Format("20060102"), expires)
	p := mutualTLSEdge(t, m)

	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(m.uploadedMTLSCertificates) != 1 {
		t.Errorf("uploads = %d, want production to upload its own despite preview's", len(m.uploadedMTLSCertificates))
	}
	if got := workerCertificateOffer(t, out).Values[edge.OfferKeyClientCertificateID]; got != "mtls-1" {
		t.Errorf("certificateId = %q, want production's own, not preview's", got)
	}
}

func TestTearingABootstrapDownDeletesItsWorkerClientCertificates(t *testing.T) {
	m := bootstrapMock(t, true)
	expires := time.Now().Add(5 * 365 * 24 * time.Hour)
	m.holdWorkerClientCertificate(t, certificateName(expires), expires)
	m.holdWorkerClientCertificate(t, certificateName(expires.Add(-48*time.Hour)), expires.Add(-48*time.Hour))
	m.holdWorkerClientCertificate(t, "ocel-origin-client-preview-"+expires.UTC().Format("20060102"), expires)
	p := mutualTLSEdge(t, m)

	if err := p.Teardown(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if !reflect.DeepEqual(m.deletedMTLSCertificates, []string{"held-1", "held-2"}) {
		t.Errorf("deleted = %v, want both production certificates and not preview's", m.deletedMTLSCertificates)
	}
}

func TestPlanningABootstrapNamesTheWorkerClientCertificate(t *testing.T) {
	t.Run("a tier with none creates one", func(t *testing.T) {
		p := mutualTLSEdge(t, bootstrapMock(t, true))
		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("planBootstrap: %v", err)
		}
		want := edge.PlanChange{Kind: kindMTLSCertificate, Name: productionCertificateBase, Action: edge.PlanCreate}
		if !slices.Contains(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a current one is kept", func(t *testing.T) {
		m := bootstrapMock(t, true)
		expires := time.Now().Add(5 * 365 * 24 * time.Hour)
		m.holdWorkerClientCertificate(t, certificateName(expires), expires)
		changes, err := mutualTLSEdge(t, m).planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("planBootstrap: %v", err)
		}
		want := edge.PlanChange{Kind: kindMTLSCertificate, Name: certificateName(expires), Action: edge.PlanKeep, Reason: reasonCurrent}
		if !slices.Contains(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("one near expiry is updated by uploading beside it", func(t *testing.T) {
		m := bootstrapMock(t, true)
		expires := time.Now().Add(100 * 24 * time.Hour)
		m.holdWorkerClientCertificate(t, certificateName(expires), expires)
		p := mutualTLSEdge(t, m)
		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("planBootstrap: %v", err)
		}
		want := edge.PlanChange{
			Kind: kindMTLSCertificate, Name: certificateName(expires), Action: edge.PlanUpdate,
			Reason: "expires " + expires.UTC().Format("2006-01-02") + "; a new one is uploaded beside it",
		}
		if !slices.Contains(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
		parts, err := p.describeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("describeBootstrap: %v", err)
		}
		for _, part := range parts {
			if part.Name == certificateName(expires) && part.Current {
				t.Errorf("a due certificate shows as current: %+v", part)
			}
		}
	})

	t.Run("an expired one is deleted", func(t *testing.T) {
		m := bootstrapMock(t, true)
		current := time.Now().Add(5 * 365 * 24 * time.Hour)
		expired := time.Now().Add(-24 * time.Hour)
		m.holdWorkerClientCertificate(t, certificateName(current), current)
		m.holdWorkerClientCertificate(t, certificateName(expired), expired)
		changes, err := mutualTLSEdge(t, m).planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("planBootstrap: %v", err)
		}
		want := edge.PlanChange{Kind: kindMTLSCertificate, Name: certificateName(expired), Action: edge.PlanDelete, Reason: "expired"}
		if !slices.Contains(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a removal deletes every held certificate", func(t *testing.T) {
		m := bootstrapMock(t, true)
		expires := time.Now().Add(5 * 365 * 24 * time.Hour)
		m.holdWorkerClientCertificate(t, certificateName(expires), expires)
		changes, err := mutualTLSEdge(t, m).planRemoveBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("planRemoveBootstrap: %v", err)
		}
		want := edge.PlanChange{Kind: kindMTLSCertificate, Name: certificateName(expires), Action: edge.PlanDelete}
		if !slices.Contains(changes, want) {
			t.Errorf("removal plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("the adoption lists the offer", func(t *testing.T) {
		adoption, err := mutualTLSEdge(t, bootstrapMock(t, true)).adoption(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("adoption: %v", err)
		}
		if !slices.Contains(adoption.Offers, edge.OfferWorkerClientCertificate) {
			t.Errorf("adoption offers = %v, want %q", adoption.Offers, edge.OfferWorkerClientCertificate)
		}
	})
}

func TestAnEdgeNotOpenedForAMutualTLSOriginKeepsNoClientCertificate(t *testing.T) {
	seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
	m := bootstrapMock(t, true)
	expires := time.Now().Add(-24 * time.Hour)
	m.holdWorkerClientCertificate(t, certificateName(expires), expires)
	p := m.provider(t)
	p.objects = func(string, r2.TemporaryCredentialNewResponse) objectAPI { return &fakeObjects{} }

	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	for _, offer := range out.Offers {
		if offer.Kind == edge.OfferWorkerClientCertificate {
			t.Errorf("offers include %q, want none", offer.Kind)
		}
	}
	if len(m.uploadedMTLSCertificates) != 0 || len(m.deletedMTLSCertificates) != 0 {
		t.Errorf("uploads = %v, deletes = %v, want no mtls_certificates call", m.uploadedMTLSCertificates, m.deletedMTLSCertificates)
	}
	changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("planBootstrap: %v", err)
	}
	for _, change := range changes {
		if change.Kind == kindMTLSCertificate {
			t.Errorf("plan lists %+v, want the plan unchanged", change)
		}
	}
	adoption, err := p.adoption(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if slices.Contains(adoption.Offers, edge.OfferWorkerClientCertificate) {
		t.Errorf("adoption offers = %v, want no %q", adoption.Offers, edge.OfferWorkerClientCertificate)
	}
	if err := p.Teardown(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(m.deletedMTLSCertificates) != 0 {
		t.Errorf("Teardown deleted %v, want no mtls_certificates call", m.deletedMTLSCertificates)
	}
}
