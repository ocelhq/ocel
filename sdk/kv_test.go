package ocel_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ocel.dev"
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

func panicOf(fn func()) (message string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			message = fmt.Sprint(recovered)
		}
	}()
	fn()
	return ""
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
	seen := discoveryDeclarations(t)

	_, file, line, _ := runtime.Caller(0)
	cache := ocel.KV("cache", ocel.KVVersion("8"), ocel.KVEviction("allkeys-lru"), ocel.KVMemory("256mb"))
	ocel.KVCounter[string](cache, "requests", "requests/:userId", ocel.KVTTL(10*time.Second))
	ocel.KVJSON[sessionKey, session](cache, "session", "session/:id")
	ocel.KVSet[memberKey](cache, "members", "rooms/:room/members/:member")

	if len(*seen) != 4 {
		t.Fatalf("declares = %d, want the store declared once and again with each entry", len(*seen))
	}
	last := (*seen)[3]
	if resource, _ := last["resource"].(map[string]any); resource["type"] != "RESOURCE_TYPE_KV" || resource["name"] != "cache" {
		t.Errorf("resource = %v, want kv cache", resource)
	}
	for _, declared := range *seen {
		if declared["source"] != fmt.Sprintf("%s:%d", file, line+1) {
			t.Errorf("source = %v, want the line ocel.KV was called on every time", declared["source"])
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
	seen := discoveryDeclarations(t)

	ocel.KV("plain")

	config, _ := (*seen)[0]["kv"].(map[string]any)
	if len(config) != 0 {
		t.Errorf("kv = %v, want nothing set", config)
	}
}

func TestOverlappingEntriesAreRefusedAtDeclarationNamingBothLines(t *testing.T) {
	discoveryDeclarations(t)

	cache := ocel.KV("overlap")
	_, file, line, _ := runtime.Caller(0)
	ocel.KVText[string](cache, "session", "session/:id")
	message := panicOf(func() { ocel.KVText[struct{}](cache, "current", "session/current") })

	for _, said := range []string{`"current"`, `"session/current"`, "overlaps", `"session/:id"`, `"session"`, fmt.Sprintf("%s:%d", file, line+1), fmt.Sprintf("%s:%d", file, line+2)} {
		if !strings.Contains(message, said) {
			t.Errorf("panic = %q, want it to say %q", message, said)
		}
	}
}

func TestAnEntryIsRefusedWhenItsNameIsTaken(t *testing.T) {
	cache := ocel.KV("taken")
	ocel.KVText[string](cache, "session", "session/:id")

	if message := panicOf(func() { ocel.KVText[string](cache, "session", "sessions/:id") }); !strings.Contains(message, `"session" is declared already`) {
		t.Errorf("panic = %q, want the second session refused", message)
	}
	if message := panicOf(func() { ocel.KVText[string](cache, "client", "clients/:id") }); !strings.Contains(message, "reserved") {
		t.Errorf("panic = %q, want client refused as reserved", message)
	}
}

func TestAnEntryNameEverySDKCannotHoldIsRefusedAtDeclaration(t *testing.T) {
	cache := ocel.KV("names")

	for _, name := range []string{"", "1st", "_hidden", "has-dash", "has.dot", "ümlaut", "a" + strings.Repeat("b", 63)} {
		message := panicOf(func() { ocel.KVText[string](cache, name, "p/:id") })
		if !strings.Contains(message, fmt.Sprintf("entry name %q is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most 63 characters", name)) {
			t.Errorf("KVText(%q) panic = %q, want the name refused by its grammar", name, message)
		}
	}
	if message := panicOf(func() { ocel.KVText[string](cache, "a"+strings.Repeat("b", 62), "q/:id") }); message != "" {
		t.Errorf("KVText(a 63-character name) panic = %q, want it accepted", message)
	}
	if message := panicOf(func() { ocel.KVText[string](cache, "Recent_2", "r/:id") }); message != "" {
		t.Errorf("KVText(Recent_2) panic = %q, want it accepted", message)
	}
}

func TestATTLWrittenWithoutItsUnitIsRefusedNamingTheUnit(t *testing.T) {
	cache := ocel.KV("unitless")
	want := "a ttl is at least 1ms, and 30ns is shorter: a TTL is a time.Duration, so write it with its unit, such as 30*time.Second"

	if message := panicOf(func() { ocel.KVText[string](cache, "session", "session/:id", ocel.KVTTL(30)) }); !strings.Contains(message, want) {
		t.Errorf("KVTTL(30) on an entry panic = %q, want it to say %q", message, want)
	}

	sessions := ocel.KVText[string](cache, "token", "token/:id")
	if err := sessions.Set(context.Background(), "a", "x", ocel.KVTTL(30)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("KVTTL(30) on a write err = %v, want it to say %q", err, want)
	}
}

func TestAMalformedPatternIsRefusedAtDeclaration(t *testing.T) {
	cache := ocel.KV("malformed")

	for pattern, says := range map[string]string{
		"session/{id}":    "hash tag",
		"session/:id/:id": "twice",
		"session/":        "empty segment",
		"session:id":      "literal",
	} {
		if message := panicOf(func() { ocel.KVText[string](cache, "e", pattern) }); !strings.Contains(message, says) {
			t.Errorf("KVText(%q) panic = %q, want it to say %q", pattern, message, says)
		}
	}
}

func TestAKeyTypeIsCheckedAgainstItsPatternAtDeclaration(t *testing.T) {
	cache := ocel.KV("keys")

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
		"a scalar for two parameters": {func() { ocel.KVText[string](cache, "a", "a/:x/:y") }, "2 parameters"},
		"a field the pattern lacks":   {func() { ocel.KVText[wrongField](cache, "b", "b/:id") }, `"id"`},
		"a field too many":            {func() { ocel.KVText[extraField](cache, "c", "c/:id") }, "Other"},
		"a field of no scalar type":   {func() { ocel.KVText[badType](cache, "d", "d/:id") }, "string or an integer"},
		"a scalar for no parameter":   {func() { ocel.KVText[string](cache, "e", "e") }, "struct{}"},
		"a float key":                 {func() { ocel.KVText[float64](cache, "f", "f/:id") }, "string or an integer"},
	} {
		if message := panicOf(check.declare); !strings.Contains(message, check.says) {
			t.Errorf("%s: panic = %q, want it to say %q", name, message, check.says)
		}
	}

	if message := panicOf(func() { ocel.KVText[sessionKey](cache, "g", "g/:id") }); message != "" {
		t.Errorf("KVText[sessionKey] panic = %q, want a struct whose field matches accepted", message)
	}
	if message := panicOf(func() { ocel.KVCounter[int](cache, "h", "h/:id") }); message != "" {
		t.Errorf("KVCounter[int] panic = %q, want an integer accepted as a one-parameter key", message)
	}
}

func TestEveryAccessorIsRefusedDuringDiscovery(t *testing.T) {
	discoveryDeclarations(t)
	cache := ocel.KV("unprovisioned")
	requests := ocel.KVCounter[string](cache, "requests", "requests/:userId")

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
	cache := ocel.KV("conn")
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
	cache := ocel.KV("late")
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

	if got, err := ocel.KV("tls").ConnectionString(); err != nil || got != "rediss://app:p%40ss%2Fword@cache.internal:6380" {
		t.Errorf("ConnectionString() = %q, %v, want rediss with the credentials escaped", got, err)
	}
	if got, err := ocel.KV("plain").ConnectionString(); err != nil || got != "redis://:pw@127.0.0.1:6379" {
		t.Errorf("ConnectionString() = %q, %v, want redis with only a password", got, err)
	}
}

func TestTheKVBindingFixtureDecodesAsTheOtherSDKsDecodeIt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "proto", "common", "bindings", "v1", "fixtures", "kv.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCEL_RESOURCE_KV_cache", string(raw))

	got, err := ocel.KV("cache").ConnectionString()
	if err != nil || got != "rediss://fixture_operator:fixture-password-not-a-secret@shop-prod-cache-h4j5k6l7.ab12cd.ng.0001.use1.cache.amazonaws.com:6380" {
		t.Errorf("ConnectionString() = %q, %v, want the fixture's host, port, credentials and TLS", got, err)
	}
	client, err := ocel.KV("cache").Client(context.Background())
	if err != nil || client.Options().TLSConfig.RootCAs == nil {
		t.Errorf("Client() = %v, want a client trusting the fixture's caPem", err)
	}
}

func TestAnOperationWithoutABindingNamesTheKeyItWaitedOn(t *testing.T) {
	cache := ocel.KV("undelivered")
	hits := ocel.KVCounter[struct{}](cache, "hits", "hits")

	var missing *ocel.MissingBindingError
	if _, err := hits.Get(context.Background(), struct{}{}); !errors.As(err, &missing) || missing.Key != "OCEL_RESOURCE_KV_undelivered" {
		t.Errorf("Get() err = %v, want the binding OCEL_RESOURCE_KV_undelivered missing", err)
	}
}
