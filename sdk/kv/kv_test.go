package kv_test

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
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ocel.dev"
	"ocel.dev/internal/sdktest"
	"ocel.dev/kv"
)

func serveKV(t *testing.T, name string) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	server.RequireUserAuth("app", "s3cret")
	host, port, _ := strings.Cut(server.Addr(), ":")
	t.Setenv("OCEL_RESOURCE_KV_"+name, fmt.Sprintf(
		`{"name":"kv--%s","kv":{"host":%q,"port":%s,"username":"app","password":"s3cret"}}`, name, host, port,
	))
	return server
}

type sessionKey struct {
	ID string
}

type memberKey struct {
	Room   string
	Member string `kv:"member"`
}

type session struct {
	User   string `json:"user"`
	Visits int    `json:"visits"`
}

func TestKVDeclaresTheStoreWithEveryEntryAttachedDuringDiscovery(t *testing.T) {
	seen := sdktest.Declarations(t)

	_, file, line, _ := runtime.Caller(0)
	cache := kv.New("cache", kv.Version("8"), kv.Eviction("allkeys-lru"), kv.Memory("256mb"))
	kv.NewCounter[string](cache, "requests", "requests/:userId", kv.TTL(10*time.Second))
	kv.NewJSON[sessionKey, session](cache, "session", "session/:id")
	kv.NewSet[memberKey](cache, "members", "rooms/:room/members/:member")

	if len(*seen) != 4 {
		t.Fatalf("declares = %d, want the store declared once and again with each entry", len(*seen))
	}
	last := (*seen)[3]
	if resource, _ := last["resource"].(map[string]any); resource["type"] != "RESOURCE_TYPE_KV" || resource["name"] != "cache" {
		t.Errorf("resource = %v, want kv cache", resource)
	}
	for _, declared := range *seen {
		if declared["source"] != fmt.Sprintf("%s:%d", file, line+1) {
			t.Errorf("source = %v, want the line kv.New was called on every time", declared["source"])
		}
	}
	config, _ := last["kv"].(map[string]any)
	if config["version"] != "8" || config["eviction"] != "allkeys-lru" || config["memory"] != "256mb" {
		t.Errorf("kv = %v, want version 8, allkeys-lru and 256mb", config)
	}
	entries, _ := config["entries"].([]any)
	want := []struct{ name, pattern, shape string }{
		{"requests", "requests/:userId", "KV_SHAPE_COUNTER"},
		{"session", "session/:id", "KV_SHAPE_JSON"},
		{"members", "rooms/:room/members/:member", "KV_SHAPE_SET"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %d", entries, len(want))
	}
	for i, w := range want {
		entry, _ := entries[i].(map[string]any)
		if entry["name"] != w.name || entry["pattern"] != w.pattern || entry["shape"] != w.shape {
			t.Errorf("entries[%d] = %v, want %+v", i, entry, w)
		}
		if entry["source"] != fmt.Sprintf("%s:%d", file, line+2+i) {
			t.Errorf("entries[%d].source = %v, want the line it was declared on", i, entry["source"])
		}
	}
}

func TestKVLeavesVersionEvictionAndMemoryToTheProvider(t *testing.T) {
	seen := sdktest.Declarations(t)

	kv.New("plain")

	config, _ := (*seen)[0]["kv"].(map[string]any)
	if len(config) != 0 {
		t.Errorf("kv = %v, want nothing set", config)
	}
}

func TestOverlappingEntriesAreRefusedAtDeclarationNamingBothLines(t *testing.T) {
	sdktest.Declarations(t)

	cache := kv.New("overlap")
	_, file, line, _ := runtime.Caller(0)
	kv.NewText[string](cache, "session", "session/:id")
	message := sdktest.PanicMessage(func() { kv.NewText[struct{}](cache, "current", "session/current") })

	for _, said := range []string{`"current"`, `"session/current"`, "overlaps", `"session/:id"`, `"session"`, fmt.Sprintf("%s:%d", file, line+1), fmt.Sprintf("%s:%d", file, line+2)} {
		if !strings.Contains(message, said) {
			t.Errorf("panic = %q, want it to say %q", message, said)
		}
	}
}

func TestAnEntryIsRefusedWhenItsNameIsTaken(t *testing.T) {
	cache := kv.New("taken")
	kv.NewText[string](cache, "session", "session/:id")

	if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "session", "sessions/:id") }); !strings.Contains(message, `"session" is declared already`) {
		t.Errorf("panic = %q, want the second session refused", message)
	}
	if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "client", "clients/:id") }); !strings.Contains(message, "reserved") {
		t.Errorf("panic = %q, want client refused as reserved", message)
	}
}

func TestAnEntryNameEverySDKCannotHoldIsRefusedAtDeclaration(t *testing.T) {
	cache := kv.New("names")

	for _, name := range []string{"", "1st", "_hidden", "has-dash", "has.dot", "ümlaut", "a" + strings.Repeat("b", 63)} {
		message := sdktest.PanicMessage(func() { kv.NewText[string](cache, name, "p/:id") })
		if !strings.Contains(message, fmt.Sprintf("entry name %q is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most 63 characters", name)) {
			t.Errorf("NewText(%q) panic = %q, want the name refused by its grammar", name, message)
		}
	}
	if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "a"+strings.Repeat("b", 62), "q/:id") }); message != "" {
		t.Errorf("NewText(a 63-character name) panic = %q, want it accepted", message)
	}
	if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "Recent_2", "r/:id") }); message != "" {
		t.Errorf("NewText(Recent_2) panic = %q, want it accepted", message)
	}
}

func TestATTLWrittenWithoutItsUnitIsRefusedNamingTheUnit(t *testing.T) {
	cache := kv.New("unitless")
	want := "a ttl is at least 1ms, and 30ns is shorter: a TTL is a time.Duration, so write it with its unit, such as 30*time.Second"

	if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "session", "session/:id", kv.TTL(30)) }); !strings.Contains(message, want) {
		t.Errorf("TTL(30) on an entry panic = %q, want it to say %q", message, want)
	}

	sessions := kv.NewText[string](cache, "token", "token/:id")
	if err := sessions.Set(context.Background(), "a", "x", kv.TTL(30)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("TTL(30) on a write err = %v, want it to say %q", err, want)
	}
}

func TestAMalformedPatternIsRefusedAtDeclaration(t *testing.T) {
	cache := kv.New("malformed")

	for pattern, says := range map[string]string{
		"session/{id}":    "hash tag",
		"session/:id/:id": "twice",
		"session/":        "empty segment",
		"session:id":      "literal",
	} {
		if message := sdktest.PanicMessage(func() { kv.NewText[string](cache, "e", pattern) }); !strings.Contains(message, says) {
			t.Errorf("NewText(%q) panic = %q, want it to say %q", pattern, message, says)
		}
	}
}

func TestAKeyTypeIsCheckedAgainstItsPatternAtDeclaration(t *testing.T) {
	cache := kv.New("keys")

	type wrongField struct{ Name string }
	type extraField struct {
		ID    string
		Other string
	}
	type badType struct{ ID float64 }

	for name, check := range map[string]struct {
		declare func()
		says    string
	}{
		"a scalar for two parameters": {func() { kv.NewText[string](cache, "a", "a/:x/:y") }, "2 parameters"},
		"a field the pattern lacks":   {func() { kv.NewText[wrongField](cache, "b", "b/:id") }, `"id"`},
		"a field too many":            {func() { kv.NewText[extraField](cache, "c", "c/:id") }, "Other"},
		"a field of no scalar type":   {func() { kv.NewText[badType](cache, "d", "d/:id") }, "string or an integer"},
		"a scalar for no parameter":   {func() { kv.NewText[string](cache, "e", "e") }, "struct{}"},
		"a float key":                 {func() { kv.NewText[float64](cache, "f", "f/:id") }, "string or an integer"},
	} {
		if message := sdktest.PanicMessage(check.declare); !strings.Contains(message, check.says) {
			t.Errorf("%s: panic = %q, want it to say %q", name, message, check.says)
		}
	}

	if message := sdktest.PanicMessage(func() { kv.NewText[sessionKey](cache, "g", "g/:id") }); message != "" {
		t.Errorf("NewText[sessionKey] panic = %q, want a struct whose field matches accepted", message)
	}
	if message := sdktest.PanicMessage(func() { kv.NewCounter[int](cache, "h", "h/:id") }); message != "" {
		t.Errorf("NewCounter[int] panic = %q, want an integer accepted as a one-parameter key", message)
	}
}

func TestEveryAccessorIsRefusedDuringDiscovery(t *testing.T) {
	sdktest.Declarations(t)
	cache := kv.New("unprovisioned")
	requests := kv.NewCounter[string](cache, "requests", "requests/:userId")

	var unprovisioned *ocel.UnprovisionedError
	if _, err := cache.Client(context.Background()); !errors.As(err, &unprovisioned) || unprovisioned.Access != "Client" {
		t.Errorf("Client() err = %v, want an UnprovisionedError for Client", err)
	}
	if _, err := cache.ConnectionString(); !errors.As(err, &unprovisioned) {
		t.Errorf("ConnectionString() err = %v, want an UnprovisionedError", err)
	}
	if _, err := requests.Increment(context.Background(), "u", 1); !errors.As(err, &unprovisioned) {
		t.Errorf("Increment() err = %v, want an UnprovisionedError", err)
	}
}

func TestClientConnectsToTheDeliveredBindingOnceAndSharesIt(t *testing.T) {
	server := serveKV(t, "conn")
	cache := kv.New("conn")
	ctx := context.Background()

	first, err := cache.Client(ctx)
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}
	second, _ := cache.Client(ctx)
	if first != second {
		t.Errorf("Client() opened a second client, want the first shared")
	}
	if err := first.Set(ctx, "hello", "world", 0).Err(); err != nil {
		t.Fatalf("SET through the client = %v", err)
	}
	if got, _ := server.Get("hello"); got != "world" {
		t.Errorf("server holds %q, want the client's write", got)
	}
}

func TestClientConnectsOnceTheBindingArrivesAfterAFailedOpen(t *testing.T) {
	cache := kv.New("late")
	ctx := context.Background()

	var missing *ocel.MissingBindingError
	if _, err := cache.Client(ctx); !errors.As(err, &missing) {
		t.Fatalf("Client() before the binding err = %v, want a *ocel.MissingBindingError", err)
	}
	serveKV(t, "late")

	client, err := cache.Client(ctx)
	if err != nil {
		t.Fatalf("Client() after the binding arrived = %v, want the client opened", err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Errorf("PING through the client = %v", err)
	}
}

func TestConnectionStringNamesTheSchemeTheBindingsTLSAsks(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_KV_tls", `{"name":"kv--tls","kv":{"host":"cache.internal","port":6380,"username":"app","password":"p@ss/word","tls":true}}`)
	t.Setenv("OCEL_RESOURCE_KV_plain", `{"name":"kv--plain","kv":{"host":"127.0.0.1","port":6379,"password":"pw"}}`)

	if got, err := kv.New("tls").ConnectionString(); err != nil || got != "rediss://app:p%40ss%2Fword@cache.internal:6380" {
		t.Errorf("ConnectionString() = %q, %v, want rediss with the credentials escaped", got, err)
	}
	if got, err := kv.New("plain").ConnectionString(); err != nil || got != "redis://:pw@127.0.0.1:6379" {
		t.Errorf("ConnectionString() = %q, %v, want redis with only a password", got, err)
	}
}

func TestTheKVBindingFixtureDecodesAsTheOtherSDKsDecodeIt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "proto", "common", "bindings", "v1", "fixtures", "kv.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCEL_RESOURCE_KV_cache", string(raw))

	got, err := kv.New("cache").ConnectionString()
	if err != nil || got != "rediss://fixture_operator:fixture-password-not-a-secret@shop-prod-cache-h4j5k6l7.ab12cd.ng.0001.use1.cache.amazonaws.com:6380" {
		t.Errorf("ConnectionString() = %q, %v, want the fixture's host, port, credentials and TLS", got, err)
	}
	client, err := kv.New("cache").Client(context.Background())
	if err != nil || client.Options().TLSConfig.RootCAs == nil {
		t.Errorf("Client() = %v, want a client trusting the fixture's caPem", err)
	}
}

func TestAnOperationWithoutABindingNamesTheKeyItWaitedOn(t *testing.T) {
	cache := kv.New("undelivered")
	hits := kv.NewCounter[struct{}](cache, "hits", "hits")

	var missing *ocel.MissingBindingError
	if _, err := hits.Get(context.Background(), struct{}{}); !errors.As(err, &missing) || missing.Key != "OCEL_RESOURCE_KV_undelivered" {
		t.Errorf("Get() err = %v, want the binding OCEL_RESOURCE_KV_undelivered missing", err)
	}
}

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

	client, err := kv.New("private").Client(ctx)
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

	client, err := kv.New("forged").Client(ctx)
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}
	if err := client.Ping(ctx).Err(); err == nil {
		t.Error("PING to a store signed by another authority = nil, want the handshake refused")
	}
}

func TestClientIsRefusedWhenTheDeliveredAuthorityIsNoCertificate(t *testing.T) {
	serveKVOverTLS(t, "garbled", newAuthority(t), "not a certificate")

	_, err := kv.New("garbled").Client(context.Background())
	if err == nil || !strings.Contains(err.Error(), "caPem") {
		t.Errorf("Client() = %v, want it refused naming the binding's caPem", err)
	}
}

func TestClientVerifiesTheTLSServerNameAForwardedBindingCarries(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_KV_forwarded", `{"name":"kv--forwarded","kv":{"host":"127.0.0.1","port":41234,"password":"pw","tls":true,"tlsServerName":"cache.internal"}}`)

	client, err := kv.New("forwarded").Client(context.Background())
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}
	if got := client.Options().TLSConfig.ServerName; got != "cache.internal" {
		t.Errorf("Client() verifies server name %q, want cache.internal: through a port forward the store's certificate still names its own host", got)
	}
	if got := client.Options().Addr; got != "127.0.0.1:41234" {
		t.Errorf("Client() connects to %q, want the forward at 127.0.0.1:41234", got)
	}
}
