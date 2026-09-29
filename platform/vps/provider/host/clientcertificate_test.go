package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
)

func pulledCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), base64.StdEncoding.EncodeToString(der)
}

func TestAShieldedHostnameIsAnsweredOnlyToAClientPresentingACertificateItTrusts(t *testing.T) {
	t.Parallel()

	certificate, trusted := pulledCertificate(t)
	table := releasing()
	table.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web"},
		{Hostname: "storage." + claimed, Owner: surface, Pointer: pointed, App: "storage"},
	}
	table.Shields = []Shield{{Hostname: claimed, Owner: surface, ClientCertificates: []string{certificate}}}

	rendered := string(mustRender(t, table))
	if strings.Count(rendered, trusted) != 1 {
		t.Errorf("the proxy config trusts the client certificate %d times, want only for %s: its store hostname is reached without the edge in front", strings.Count(rendered, trusted), claimed)
	}

	written, err := WriteRoutingTable(table)
	if err != nil {
		t.Fatalf("WriteRoutingTable: %v", err)
	}
	read, err := ReadRoutingTable(written)
	if err != nil {
		t.Fatalf("ReadRoutingTable: %v", err)
	}
	if len(read.Shields) != 1 || !slices.Equal(read.Shields[0].ClientCertificates, []string{certificate}) {
		t.Errorf("the table reads back shields %+v, want the client certificate kept: the switchboard reads the same table and must accept it", read.Shields)
	}
}

func TestShieldingAHostnameCarriesItsSuccessorCertificateToEveryHostnameThatTrustedTheOneItReplaces(t *testing.T) {
	t.Parallel()

	shields := []Shield{
		{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"old"}},
		{Hostname: "blog.example.com", Owner: "ocel-blog-production", ClientCertificates: []string{"old"}},
		{Hostname: "other.example.org", Owner: "ocel-other-production", ClientCertificates: []string{"another zone"}},
	}

	rotated := Shielding(shields, Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"new", "old"}})
	trusts := map[string][]string{}
	for _, shield := range rotated {
		trusts[shield.Hostname] = shield.ClientCertificates
	}
	if !slices.Equal(trusts["blog.example.com"], []string{"new", "old"}) {
		t.Errorf("blog.example.com trusts %v, want the successor too: the zone presents one certificate to every origin it forwards to, and switches them all at once", trusts["blog.example.com"])
	}
	if !slices.Equal(trusts["other.example.org"], []string{"another zone"}) {
		t.Errorf("other.example.org trusts %v, want its own zone's certificate left alone", trusts["other.example.org"])
	}

	retired := Shielding(rotated, Shield{Hostname: "shop.example.com", Owner: "ocel-shop-production", ClientCertificates: []string{"new"}})
	for _, shield := range retired {
		if shield.Hostname == "blog.example.com" && !slices.Equal(shield.ClientCertificates, []string{"new"}) {
			t.Errorf("blog.example.com trusts %v once the old certificate is retired, want the successor alone", shield.ClientCertificates)
		}
	}
}
