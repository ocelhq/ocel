package inlinebinding

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestConnConfig(t *testing.T) {
	t.Run("a url is read whole, keeping its sslmode and options", func(t *testing.T) {
		config, err := ConnConfig(&bindingsv1.PostgresProperties{Url: "postgres://app:pw@ep-cool.neon.tech:5432/orders?sslmode=require&options=endpoint%3Dep-cool"})
		if err != nil {
			t.Fatalf("ConnConfig: %v", err)
		}
		if config.Host != "ep-cool.neon.tech" || config.Database != "orders" || config.User != "app" || config.Password != "pw" {
			t.Errorf("config = %s@%s/%s, want the url's own fields", config.User, config.Host, config.Database)
		}
		if config.TLSConfig == nil {
			t.Error("TLSConfig = nil, want sslmode=require honoured")
		}
		if config.RuntimeParams["options"] != "endpoint=ep-cool" {
			t.Errorf("options = %q, want the pooler's endpoint option kept", config.RuntimeParams["options"])
		}
	})

	t.Run("verify-full checks the server against the CA the binding names", func(t *testing.T) {
		config, err := ConnConfig(&bindingsv1.PostgresProperties{
			Host: "db.example.com", Port: 6543, Database: "orders", Username: "app", Password: "p@ss:word/#?",
			TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL, TlsCa: selfSignedPEM(t),
		})
		if err != nil {
			t.Fatalf("ConnConfig: %v", err)
		}
		if config.Password != "p@ss:word/#?" || config.Port != 6543 {
			t.Errorf("config password/port = %q/%d, want them passed through exactly", config.Password, config.Port)
		}
		if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != "db.example.com" {
			t.Fatalf("TLSConfig = %+v, want the hostname verified", config.TLSConfig)
		}
		if config.TLSConfig.RootCAs == nil {
			t.Error("RootCAs = nil, want the binding's CA trusted")
		}
		if len(config.Fallbacks) != 0 {
			t.Errorf("Fallbacks = %d, want no plaintext fallback under verify-full", len(config.Fallbacks))
		}
	})

	t.Run("a CA containing no certificate is refused", func(t *testing.T) {
		_, err := ConnConfig(&bindingsv1.PostgresProperties{Host: "db", Port: 5432, Database: "d", Username: "u", Password: "p", TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL, TlsCa: "not a pem"})
		if err == nil {
			t.Fatal("ConnConfig = nil error, want the CA refused")
		}
	})
}

func TestProbePostgresAgainstALiveServer(t *testing.T) {
	live := os.Getenv("OCEL_TEST_POSTGRES_URL")
	if live == "" {
		t.Skip("set OCEL_TEST_POSTGRES_URL to a reachable postgres to run this")
	}
	version, err := ProbePostgres(context.Background(), &bindingsv1.PostgresProperties{Url: live})
	if err != nil {
		t.Fatalf("ProbePostgres: %v", err)
	}
	if version < 100000 {
		t.Errorf("server_version_num = %d, want a postgres 10 or later", version)
	}
}
