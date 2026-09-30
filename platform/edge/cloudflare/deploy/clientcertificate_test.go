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
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestTheZonesClientCertificateVerifiesAgainstTheCAsEnsureReturnsAsALoadBalancerInVerifyModeChecksIt(t *testing.T) {
	m := proxyZoneMock()

	authorities, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(m.clientCertificates) != 1 || len(authorities) != 1 {
		t.Fatalf("Ensure = %d CAs with %d certificates uploaded, want one of each", len(authorities), len(m.clientCertificates))
	}
	presented := parsedLeaf(t, m.clientCertificates[0]["certificate"].(string))
	if presented.IsCA {
		t.Error("the certificate the zone presents is a CA, and Cloudflare refuses anything but a leaf for zone-level authenticated origin pulls")
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
		t.Errorf("the certificate the zone presents does not verify as a client against the CAs Ensure returned: %v", err)
	}
}

func TestTheCloudflareProxyTrustsASelfSignedCertificateAZoneHoldsAsItsOwnCA(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	selfSigned := mintSelfSignedClientCertificate(t)
	m.clientCertificates = []map[string]any{{"id": "yours", "certificate": selfSigned, "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}}

	authorities, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), "shop.app.com")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !slices.Equal(authorities, []string{selfSigned}) {
		t.Errorf("Ensure = %v, want the self-signed certificate the zone holds, which is the one thing it chains to", authorities)
	}
}

func TestTheCloudflareProxyRefusesAZoneHoldingACertificateWhoseCAItCannotRead(t *testing.T) {
	m := proxyZoneMock()
	m.originPulls = true
	m.clientCertificates = []map[string]any{{"id": "yours", "certificate": mintIssuedClientCertificate(t), "status": "active", "uploaded_on": "2026-01-01T00:00:00Z"}}

	_, err := m.proxy(t).Hooks().ClientCertificates.Ensure(context.Background(), "shop.app.com")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, "yours") {
		t.Errorf("Ensure = %v, want an invalid refusal naming the certificate whose CA no origin could be told to trust", err)
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

func mintIssuedClientCertificate(t *testing.T) string {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "your CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "your client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mintHeldClientCertificate(t *testing.T, now time.Time) (certificate, authority string) {
	t.Helper()
	certificate, _, err := mintClientCertificate("app.com", now)
	if err != nil {
		t.Fatal(err)
	}
	authority, err = readClientCA("app.com", zoneClientCertificate{ID: "held", Certificate: certificate})
	if err != nil {
		t.Fatal(err)
	}
	return certificate, authority
}
