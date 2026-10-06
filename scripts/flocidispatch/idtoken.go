package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"time"
)

const (
	idTokenIssuer = "https://accounts.google.com"
	idTokenLife   = time.Hour
	signingBits   = 2048
)

type signingKey struct {
	private *rsa.PrivateKey
	kid     string
}

func newSigningKey() (*signingKey, error) {
	private, err := rsa.GenerateKey(rand.Reader, signingBits)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(private.N.Bytes())
	return &signingKey{private: private, kid: hex.EncodeToString(sum[:])[:16]}, nil
}

func (k *signingKey) signIDToken(email, audience string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": k.kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":            idTokenIssuer,
		"aud":            audience,
		"email":          email,
		"email_verified": true,
		"sub":            email,
		"iat":            now.Unix(),
		"exp":            now.Add(idTokenLife).Unix(),
	})
	if err != nil {
		return "", err
	}
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, k.private, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (k *signingKey) publicKeys() ([]byte, error) {
	return json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": k.kid,
		"alg": "RS256",
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(k.private.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.private.E)).Bytes()),
	}}})
}
