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

func TestEveryTokenVectorIsAcceptedOrRefusedForTheReasonItNames(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	if len(vectors.Cases) == 0 {
		t.Fatal("the token vectors hold no verifier cases")
	}
	for _, c := range vectors.Cases {
		_, err := token.Verify(c.Token, vectors.VerifyKey, time.Unix(vectors.Now, 0), vectors.expected())
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

func TestEveryMintVectorIsTheTokenTheTestMinterSigns(t *testing.T) {
	t.Parallel()

	vectors := readTokenVectors(t)
	key := ed25519.NewKeyFromSeed(vectors.SigningKey)
	for _, c := range vectors.Mint {
		var claims token.Claims
		if err := json.Unmarshal(c.Claims, &claims); err != nil {
			t.Fatalf("%s: decode claims: %v", c.Name, err)
		}
		if minted := tokentest.Sign(t, key, claims); minted != c.Token {
			t.Errorf("%s: Sign = %q, want %q", c.Name, minted, c.Token)
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
	_, err := token.Verify(minted, vectors.VerifyKey, time.Unix(vectors.Now, 0), want)

	var refused *token.Refusal
	if !errors.As(err, &refused) || refused.Reason != token.ReasonOperation {
		t.Fatalf("Verify = %v, want a subscribe token refused as a publish token", err)
	}
}
