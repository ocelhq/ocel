package ocel_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"

	"ocel.dev"
)

func postgresRecord(t *testing.T, properties map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "main", "postgres": properties})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", string(raw))
}

func testCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestARecordsURLIsTheConnectionVerbatim(t *testing.T) {
	const dsn = "postgres://app:s3cret@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool"
	postgresRecord(t, map[string]any{"url": dsn})
	db := ocel.Postgres("main")

	got, err := db.ConnectionString()
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	if got != dsn {
		t.Errorf("ConnectionString() = %q, want the record's url verbatim", got)
	}
	pool, err := db.Pool(t.Context())
	if err != nil {
		t.Fatalf("Pool() error = %v", err)
	}
	defer pool.Close()
	config := pool.Config().ConnConfig
	if config.Host != "ep-cool.neon.tech" || config.RuntimeParams["options"] != "endpoint=ep-cool" || config.TLSConfig == nil {
		t.Errorf("pool config = host %q options %q tls %v, want the url's host, options and sslmode", config.Host, config.RuntimeParams["options"], config.TLSConfig != nil)
	}
}

func TestARecordRequiringTLSEncryptsWithoutVerifying(t *testing.T) {
	postgresRecord(t, map[string]any{"host": "db", "port": 5432, "database": "d", "username": "u", "password": "p", "tlsMode": "require"})
	db := ocel.Postgres("main")

	got, err := db.ConnectionString()
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	parsed, _ := url.Parse(got)
	if parsed.Query().Get("sslmode") != "require" {
		t.Errorf("ConnectionString() = %q, want sslmode=require", got)
	}
	pool, err := db.Pool(t.Context())
	if err != nil {
		t.Fatalf("Pool() error = %v", err)
	}
	defer pool.Close()
	config := pool.Config().ConnConfig
	if config.TLSConfig == nil || len(config.Fallbacks) != 0 {
		t.Errorf("pool tls = %v with %d fallbacks, want tls and no plaintext fallback", config.TLSConfig, len(config.Fallbacks))
	}
}

func TestARecordUnderVerifyFullTrustsItsOwnCA(t *testing.T) {
	postgresRecord(t, map[string]any{
		"host": "db.example.com", "port": 5432, "database": "d", "username": "u", "password": "p",
		"tlsMode": "verify-full", "tlsCa": testCA(t),
	})
	pool, err := ocel.Postgres("main").Pool(t.Context())
	if err != nil {
		t.Fatalf("Pool() error = %v", err)
	}
	defer pool.Close()
	tls := pool.Config().ConnConfig.TLSConfig
	if tls == nil || tls.InsecureSkipVerify || tls.ServerName != "db.example.com" || tls.RootCAs == nil {
		t.Errorf("pool tls = %+v, want the hostname verified against the record's CA", tls)
	}
}
