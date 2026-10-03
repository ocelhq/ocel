package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

const (
	testAPIID = "abcdefghijklmnopqrstuvwxyz"
	testHost  = "xyz123.appsync-api.us-east-1.amazonaws.com"
)

var testNow = time.Unix(1_800_000_000, 0)

func newTestKey(t *testing.T, seed byte) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed([]byte(strings.Repeat(string(rune('a'+seed)), ed25519.SeedSize)))
}

func newTestAuthorizer(t *testing.T, keys map[string]ed25519.PrivateKey) *authorizer {
	t.Helper()
	encoded := map[string]string{}
	for ns, key := range keys {
		encoded[ns] = base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	verifyKeys, err := readVerifyKeys(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &authorizer{
		keys: verifyKeys,
		hosts: newAPIHosts(func(_ context.Context, apiID string) (string, error) {
			if apiID != testAPIID {
				return "", errors.New("no such api")
			}
			return testHost, nil
		}),
		now: func() time.Time { return testNow },
	}
}

func sign(t *testing.T, key ed25519.PrivateKey, op token.Operation, ns, channel string) string {
	t.Helper()
	return tokentest.Sign(t, key, token.Claims{
		Audience:  testHost,
		IssuedAt:  testNow.Unix() - 5,
		ExpiresAt: testNow.Unix() + 55,
		Issuer:    "ocel:rt:" + ns,
		ID:        "jti",
		Subject:   "u_1",
		Ocel:      token.Grant{Channel: channel, Namespace: ns, Operation: op},
	})
}

func connectRequest(raw string) authorizationRequest {
	return authorizationRequest{
		AuthorizationToken: raw,
		RequestContext:     requestContext{APIID: testAPIID, Operation: operationConnect},
	}
}

func subscribeRequest(raw, ns, channel string) authorizationRequest {
	return authorizationRequest{
		AuthorizationToken: raw,
		RequestContext:     requestContext{APIID: testAPIID, Operation: operationSubscribe, ChannelNamespaceName: ns, Channel: channel},
	}
}

func TestAuthorizeAdmitsOnlyTokensMintedForTheOperationAndChannel(t *testing.T) {
	app, chat := newTestKey(t, 0), newTestKey(t, 1)
	a := newTestAuthorizer(t, map[string]ed25519.PrivateKey{"app": app, "chat": chat})
	expired := tokentest.Sign(t, app, token.Claims{
		Audience: testHost, IssuedAt: testNow.Unix() - 120, ExpiresAt: testNow.Unix() - 60,
		Ocel: token.Grant{Channel: "/app/orders/o1", Namespace: "app", Operation: token.Subscribe},
	})
	foreignAudience := tokentest.Sign(t, app, token.Claims{
		Audience: "elsewhere.appsync-api.us-east-1.amazonaws.com", IssuedAt: testNow.Unix(), ExpiresAt: testNow.Unix() + 60,
		Ocel: token.Grant{Channel: "/app", Namespace: "app", Operation: token.Connect},
	})

	for _, tc := range []struct {
		name string
		req  authorizationRequest
		want bool
	}{
		{"a connect token for the namespace", connectRequest(sign(t, app, token.Connect, "app", "/app")), true},
		{"a connect token of a second namespace, checked with its own key", connectRequest(sign(t, chat, token.Connect, "chat", "/chat")), true},
		{"a connect token signed with another namespace's key", connectRequest(sign(t, chat, token.Connect, "app", "/app")), false},
		{"a connect token for a namespace this api serves no key for", connectRequest(sign(t, app, token.Connect, "gone", "/gone")), false},
		{"a subscribe token presented at connect", connectRequest(sign(t, app, token.Subscribe, "app", "/app")), false},
		{"a connect token minted for another transport host", connectRequest(foreignAudience), false},
		{"a token that is no JWT", connectRequest("not-a-token"), false},
		{"a subscribe token for the channel", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/o1"), "app", "/app/orders/o1"), true},
		{"a subscribe token for a wildcard channel", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/*"), "app", "/app/orders/*"), true},
		{"a subscribe whose channel came without its leading slash", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/o1"), "app", "app/orders/o1"), true},
		{"a subscribe whose channel came with a trailing slash", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/o1"), "app", "/app/orders/o1/"), true},
		{"a subscribe token for another channel", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/o2"), "app", "/app/orders/o1"), false},
		{"a subscribe token for another namespace's channel", subscribeRequest(sign(t, app, token.Subscribe, "app", "/app/orders/o1"), "chat", "/app/orders/o1"), false},
		{"an expired subscribe token", subscribeRequest(expired, "app", "/app/orders/o1"), false},
		{"a connect token presented at subscribe", subscribeRequest(sign(t, app, token.Connect, "app", "/app"), "app", "/app"), false},
		{"a publish, which only the app's role may make", authorizationRequest{
			AuthorizationToken: sign(t, app, token.Publish, "app", "/app/orders/o1"),
			RequestContext:     requestContext{APIID: testAPIID, Operation: "EVENT_PUBLISH", ChannelNamespaceName: "app", Channel: "/app/orders/o1"},
		}, false},
		{"a request from an api whose host cannot be read", authorizationRequest{
			AuthorizationToken: sign(t, app, token.Connect, "app", "/app"),
			RequestContext:     requestContext{APIID: "another", Operation: operationConnect},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := a.authorize(context.Background(), tc.req)
			if got.IsAuthorized != tc.want {
				t.Fatalf("authorize = %+v, want isAuthorized %v", got, tc.want)
			}
		})
	}
}

func TestAuthorizeAsksAppSyncNeverToCacheAnAnswer(t *testing.T) {
	app := newTestKey(t, 0)
	a := newTestAuthorizer(t, map[string]ed25519.PrivateKey{"app": app})
	for _, req := range []authorizationRequest{
		connectRequest(sign(t, app, token.Connect, "app", "/app")),
		connectRequest("not-a-token"),
	} {
		encoded, err := json.Marshal(a.authorize(context.Background(), req))
		if err != nil {
			t.Fatal(err)
		}
		var answer map[string]any
		if err := json.Unmarshal(encoded, &answer); err != nil {
			t.Fatal(err)
		}
		if ttl, present := answer["ttlOverride"]; !present || ttl != float64(0) {
			t.Errorf("answer = %s, want ttlOverride 0 so a revoked or expired token is never admitted from cache", encoded)
		}
	}
}

func TestAPIHostsReadsEachAPIOnce(t *testing.T) {
	reads := 0
	hosts := newAPIHosts(func(context.Context, string) (string, error) {
		reads++
		if reads == 1 {
			return "", errors.New("throttled")
		}
		return testHost, nil
	})
	if _, err := hosts.find(context.Background(), testAPIID); err == nil {
		t.Fatal("find = nil error, want the failed read passed on")
	}
	for range 3 {
		host, err := hosts.find(context.Background(), testAPIID)
		if err != nil || host != testHost {
			t.Fatalf("find = %q, %v, want %q", host, err, testHost)
		}
	}
	if reads != 2 {
		t.Errorf("reads = %d, want a failed read retried and a found host kept", reads)
	}
}

func TestReadVerifyKeysRefusesAKeyThatIsNotEd25519(t *testing.T) {
	for _, raw := range []string{
		"",
		"not json",
		`{}`,
		`{"app":"not base64!"}`,
		`{"app":"` + base64.StdEncoding.EncodeToString([]byte("short")) + `"}`,
	} {
		if _, err := readVerifyKeys(raw); err == nil {
			t.Errorf("readVerifyKeys(%q) = nil error, want a refusal", raw)
		}
	}
}
