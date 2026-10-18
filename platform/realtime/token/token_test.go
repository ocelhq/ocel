package token_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/token"
	"github.com/ocelhq/ocel/platform/realtime/token/tokentest"
)

type tokenVectors struct {
	SigningKey []byte `json:"signingKey"`
	VerifyKey  []byte `json:"verifyKey"`
	Now        int64  `json:"now"`
	Expect     struct {
		Audience  string `json:"aud"`
		Namespace string `json:"ns"`
		Channel   string `json:"ch"`
	} `json:"expect"`
	Cases []struct {
		Name   string `json:"name"`
		Token  string `json:"token"`
		Valid  bool   `json:"valid"`
		Reason string `json:"reason"`
	} `json:"cases"`
	Mint []struct {
		Name   string          `json:"name"`
		Claims json.RawMessage `json:"claims"`
		Token  string          `json:"token"`
	} `json:"mint"`
}

func readTokenVectors(t *testing.T) tokenVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "realtime", "vectors.json"))
	if err != nil {
		t.Fatalf("read the shared realtime vectors: %v", err)
	}
	var v struct {
		Tokens tokenVectors `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode the shared realtime vectors: %v", err)
	}
	return v.Tokens
}

func (v tokenVectors) expected() token.Expected {
	return token.Expected{Audience: v.Expect.Audience, Namespace: v.Expect.Namespace, Operation: token.Subscribe, Channel: v.Expect.Channel}
}

func (v tokenVectors) keys(namespace string) (ed25519.PublicKey, bool) {
	return v.VerifyKey, namespace == v.Expect.Namespace
}

func TestAConnectTokenIsVerifiedWithTheKeyOfTheNamespaceItNamesForThatNamespacesChannelAlone(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_790_000_000, 0)
	app, chat := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), ed25519.NewKeyFromSeed([]byte("chat-chat-chat-chat-chat-chat-ch"))
	keys := func(namespace string) (ed25519.PublicKey, bool) {
		switch namespace {
		case "app":
			return app.Public().(ed25519.PublicKey), true
		case "chat":
			return chat.Public().(ed25519.PublicKey), true
		}
		return nil, false
	}
	mint := func(key ed25519.PrivateKey, namespace, channel string) string {
		return tokentest.Sign(t, key, token.Claims{
			Audience:  "realtime.shop.example",
			ExpiresAt: now.Add(time.Minute).Unix(),
			Ocel:      token.Grant{Channel: channel, Namespace: namespace, Operation: token.Connect},
		})
	}
	want := token.Expected{Audience: "realtime.shop.example", Operation: token.Connect}

	for name, accepted := range map[string]string{"app": mint(app, "app", "/app"), "chat": mint(chat, "chat", "/chat")} {
		claims, err := token.Verify(accepted, keys, now, want)
		if err != nil || claims.Ocel.Namespace != name {
			t.Errorf("Verify of a connect token for %s = %+v, %v, want it accepted for %s", name, claims, err, name)
		}
	}
	for name, refused := range map[string]struct {
		raw    string
		reason token.Reason
	}{
		"signed with another namespace's key": {mint(chat, "app", "/app"), token.ReasonSignature},
		"for a namespace with no key":         {mint(app, "gone", "/gone"), token.ReasonNamespace},
		"for another namespace's channel":     {mint(app, "app", "/chat"), token.ReasonChannel},
		"for a channel below its namespace":   {mint(app, "app", "/app/orders"), token.ReasonChannel},
	} {
		_, err := token.Verify(refused.raw, keys, now, want)
		var refusal *token.Refusal
		if !errors.As(err, &refusal) || refusal.Reason != refused.reason {
			t.Errorf("Verify of a connect token %s = %v, want it refused for %q", name, err, refused.reason)
		}
	}
}

func TestEveryTokenVectorIsAcceptedOrRefusedForTheReasonItNames(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	if len(vectors.Cases) == 0 {
		t.Fatal("the token vectors hold no verifier cases")
	}
	for _, c := range vectors.Cases {
		_, err := token.Verify(c.Token, vectors.keys, time.Unix(vectors.Now, 0), vectors.expected())
		if c.Valid {
			if err != nil {
				t.Errorf("%s: Verify = %v, want accepted", c.Name, err)
			}
			continue
		}
		var refused *token.Refusal
		if !errors.As(err, &refused) || string(refused.Reason) != c.Reason {
			t.Errorf("%s: Verify = %v, want refused for %q", c.Name, err, c.Reason)
		}
	}
}

func TestEveryMintVectorIsTheTokenSignMints(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	key := ed25519.NewKeyFromSeed(vectors.SigningKey)
	for _, c := range vectors.Mint {
		var claims token.Claims
		if err := json.Unmarshal(c.Claims, &claims); err != nil {
			t.Fatalf("%s: decode claims: %v", c.Name, err)
		}
		minted, err := token.Sign(key, claims)
		if err != nil || minted != c.Token {
			t.Errorf("%s: Sign = %q, %v, want %q", c.Name, minted, err, c.Token)
		}
	}
}

func TestATokenMintedForOneOperationIsRefusedForAnother(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	want := vectors.expected()
	claims := token.Claims{
		Audience:  want.Audience,
		ExpiresAt: vectors.Now + 60,
		Ocel:      token.Grant{Channel: want.Channel, Namespace: want.Namespace, Operation: token.Subscribe},
	}
	minted := tokentest.Sign(t, ed25519.NewKeyFromSeed(vectors.SigningKey), claims)

	want.Operation = token.Publish
	_, err := token.Verify(minted, vectors.keys, time.Unix(vectors.Now, 0), want)

	var refused *token.Refusal
	if !errors.As(err, &refused) || refused.Reason != token.ReasonOperation {
		t.Fatalf("Verify = %v, want a subscribe token refused as a publish token", err)
	}
}

func TestATokenMintedForAnotherSubjectIsRefusedWhereOneSubjectIsExpected(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	want := vectors.expected()
	key := ed25519.NewKeyFromSeed(vectors.SigningKey)
	mint := func(subject string) string {
		return tokentest.Sign(t, key, token.Claims{
			Audience:  want.Audience,
			ExpiresAt: vectors.Now + 60,
			Subject:   subject,
			Ocel:      token.Grant{Channel: want.Channel, Namespace: want.Namespace, Operation: token.Subscribe},
		})
	}
	want.Subject = "server"

	if _, err := token.Verify(mint("server"), vectors.keys, time.Unix(vectors.Now, 0), want); err != nil {
		t.Fatalf("Verify of a server token = %v, want accepted", err)
	}
	_, err := token.Verify(mint("user-1"), vectors.keys, time.Unix(vectors.Now, 0), want)
	var refused *token.Refusal
	if !errors.As(err, &refused) || refused.Reason != token.ReasonSubject {
		t.Fatalf("Verify = %v, want a user's token refused where a server token is expected", err)
	}
}
