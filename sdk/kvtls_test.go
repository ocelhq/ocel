package ocel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ocel.dev"
)

type authority struct {
	pem  string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newAuthority(t *testing.T) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kv test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return authority{pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), cert: cert, key: key}
}

func (a authority) serverCertificate(t *testing.T, ip net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: ip.String()},
		IPAddresses:  []net.IP{ip},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func serveKVOverTLS(t *testing.T, name string, signer authority, delivered string) {
	t.Helper()
	server := miniredis.NewMiniRedis()
	server.RequireUserAuth("app", "s3cret")
	certificate := signer.serverCertificate(t, net.IPv4(127, 0, 0, 1))
	if err := server.StartTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	host, port, _ := strings.Cut(server.Addr(), ":")
	authorityJSON, err := json.Marshal(delivered)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCEL_RESOURCE_KV_"+name, `{"name":"kv--`+name+`","kv":{"host":"`+host+`","port":`+port+
		`,"username":"app","password":"s3cret","tls":true,"caPem":`+string(authorityJSON)+`}}`)
}

func TestClientTrustsTheCertificateAuthorityTheBindingDelivers(t *testing.T) {
	signer := newAuthority(t)
	serveKVOverTLS(t, "private", signer, signer.pem)
	ctx := context.Background()

	client, err := ocel.KV("private").Client(ctx)
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Errorf("PING over TLS to a store whose certificate the delivered authority signed = %v, want it answered", err)
	}
}

func TestClientRefusesAStoreWhoseCertificateTheDeliveredAuthorityDidNotSign(t *testing.T) {
	serveKVOverTLS(t, "forged", newAuthority(t), newAuthority(t).pem)
	ctx := context.Background()

	client, err := ocel.KV("forged").Client(ctx)
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}
	if err := client.Ping(ctx).Err(); err == nil {
		t.Error("PING to a store signed by another authority = nil, want the handshake refused")
	}
}

func TestClientIsRefusedWhenTheDeliveredAuthorityIsNoCertificate(t *testing.T) {
	serveKVOverTLS(t, "garbled", newAuthority(t), "not a certificate")

	_, err := ocel.KV("garbled").Client(context.Background())
	if err == nil || !strings.Contains(err.Error(), "caPem") {
		t.Errorf("Client() = %v, want it refused naming the binding's caPem", err)
	}
}
