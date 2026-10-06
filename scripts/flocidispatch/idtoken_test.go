package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	refreshAccount  = "refresh@p.iam.gserviceaccount.com"
	refreshAudience = "https://web-1.europe-west1.run.app/_ocel/refresh"
)

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func publishedKeys(t *testing.T, key *signingKey) []jwk {
	t.Helper()
	raw, err := key.publicKeys()
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("publicKeys() = %s, not a JWKS: %v", raw, err)
	}
	return set.Keys
}

func decodeSegment(t *testing.T, segment string, into any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("segment %q is not unpadded base64url: %v", segment, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func TestASignedIDTokenCarriesTheClaimsGooglesTokenCheckReads(t *testing.T) {
	key, err := newSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)

	token, err := key.signIDToken(refreshAccount, refreshAudience, now)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d segments, want 3", len(parts))
	}
	var header struct {
		Alg, Kid, Typ string
	}
	decodeSegment(t, parts[0], &header)
	var claims struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Sub           string `json:"sub"`
		Iat           int64  `json:"iat"`
		Exp           int64  `json:"exp"`
	}
	decodeSegment(t, parts[1], &claims)
	if header.Alg != "RS256" || header.Kid == "" || header.Typ != "JWT" {
		t.Errorf("the header is %+v, want RS256 with a kid", header)
	}
	if claims.Iss != "https://accounts.google.com" || claims.Aud != refreshAudience || claims.Email != refreshAccount ||
		!claims.EmailVerified || claims.Sub != refreshAccount {
		t.Errorf("the claims are %+v, want Google's issuer, the audience, and the verified account", claims)
	}
	if claims.Iat != now.Unix() || claims.Exp != now.Add(time.Hour).Unix() {
		t.Errorf("iat=%d exp=%d, want now and an hour on", claims.Iat, claims.Exp)
	}

	keys := publishedKeys(t, key)
	if len(keys) != 1 || keys[0].Kid != header.Kid {
		t.Fatalf("the published keys %+v do not hold the key %q", keys, header.Kid)
	}
	n, _ := base64.RawURLEncoding.DecodeString(keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(keys[0].E)
	public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("the published key does not verify the token: %v", err)
	}
}

func TestThePublicKeysHoldTheKeyTokensAreSignedWith(t *testing.T) {
	key, err := newSigningKey()
	if err != nil {
		t.Fatal(err)
	}

	keys := publishedKeys(t, key)

	if len(keys) != 1 {
		t.Fatalf("the JWKS holds %d keys, want one", len(keys))
	}
	published := keys[0]
	if published.Kty != "RSA" || published.Alg != "RS256" || published.Use != "sig" || published.Kid != key.kid {
		t.Errorf("the published key is %+v, want an RS256 signing key named %q", published, key.kid)
	}
	if want := base64.RawURLEncoding.EncodeToString(key.private.N.Bytes()); published.N != want {
		t.Errorf("n = %q, want the private key's modulus", published.N)
	}
	if want := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.private.E)).Bytes()); published.E != want {
		t.Errorf("e = %q, want the private key's exponent", published.E)
	}
}
