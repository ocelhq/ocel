package realtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"ocel.dev/internal/codec"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

type realtimeOperation string

const (
	realtimeConnect   realtimeOperation = "connect"
	realtimeSubscribe realtimeOperation = "subscribe"
	realtimePublish   realtimeOperation = "publish"
)

type mintedToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

func signToken(signingKey []byte, header, claims any) (string, error) {
	if len(signingKey) != ed25519.SeedSize {
		return "", fmt.Errorf("the realtime signing key is %d bytes, and an Ed25519 seed is %d", len(signingKey), ed25519.SeedSize)
	}
	encodedHeader, err := codec.EncodeJSON(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := codec.EncodeJSON(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." + base64.RawURLEncoding.EncodeToString(encodedClaims)
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(signingKey), []byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (d *Realtime) mintToken(properties *bindingsv1.RealtimeProperties, subject string, operation realtimeOperation, channel string) (mintedToken, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return mintedToken{}, err
	}
	issuedAt := time.Now().Unix()
	expiresAt := issuedAt + int64(d.settings.ttl/time.Second)
	token, err := signToken(properties.GetSigningKey(), map[string]any{"alg": "EdDSA", "typ": "JWT"}, map[string]any{
		"iss": "ocel:rt:" + d.name,
		"aud": properties.GetHost(),
		"sub": subject,
		"iat": issuedAt,
		"exp": expiresAt,
		"jti": base64.RawURLEncoding.EncodeToString(id),
		"ocel": map[string]any{
			"op": operation,
			"ch": channel,
			"ns": d.name,
		},
	})
	if err != nil {
		return mintedToken{}, err
	}
	return mintedToken{Token: token, ExpiresAt: expiresAt}, nil
}
