package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
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

func TestAHostnameClaimedWithAClientCertificateIsAnsweredOnlyToAClientPresentingIt(t *testing.T) {
	t.Parallel()

	certificate, trusted := pulledCertificate(t)
	table := releasing()
	table.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "web", ClientCertificate: certificate},
		{Hostname: "storage." + claimed, Owner: surface, Pointer: pointed, App: "storage"},
	}

	rendered := string(mustRender(t, table))
	if !strings.Contains(rendered, trusted) {
		t.Errorf("the proxy config trusts no client certificate for %s:\n%s", claimed, rendered)
	}
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
	if read.Claims[0].ClientCertificate != certificate && read.Claims[1].ClientCertificate != certificate {
		t.Errorf("the table reads back claims %+v, want the client certificate kept: the switchboard reads the same table and must accept it", read.Claims)
	}
}
