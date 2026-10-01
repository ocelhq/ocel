package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Operation string

const (
	Connect   Operation = "connect"
	Subscribe Operation = "subscribe"
	Publish   Operation = "publish"
)

const Algorithm = "EdDSA"

type Header struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type Claims struct {
	Audience  string `json:"aud"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Issuer    string `json:"iss"`
	ID        string `json:"jti"`
	Ocel      Grant  `json:"ocel"`
	Subject   string `json:"sub"`
}

type Grant struct {
	Channel   string    `json:"ch"`
	Namespace string    `json:"ns"`
	Operation Operation `json:"op"`
}

type Expected struct {
	Audience  string
	Namespace string
	Operation Operation
	Channel   string
}

type Reason string

const (
	ReasonMalformed Reason = "malformed"
	ReasonAlgorithm Reason = "algorithm"
	ReasonSignature Reason = "signature"
	ReasonExpired   Reason = "expired"
	ReasonAudience  Reason = "audience"
	ReasonNamespace Reason = "namespace"
	ReasonOperation Reason = "operation"
	ReasonChannel   Reason = "channel"
)

type Refusal struct {
	Reason Reason
}

func (r *Refusal) Error() string {
	switch r.Reason {
	case ReasonMalformed:
		return "the token is no JWT of a header, claims and a signature"
	case ReasonAlgorithm:
		return "the token is not signed with " + Algorithm
	case ReasonSignature:
		return "the token's signature does not verify against this realtime resource's key"
	case ReasonExpired:
		return "the token has expired"
	default:
		return fmt.Sprintf("the token was minted for another %s", r.Reason)
	}
}

func refuse(reason Reason) error { return &Refusal{Reason: reason} }

func Verify(raw string, key ed25519.PublicKey, now time.Time, want Expected) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, refuse(ReasonMalformed)
	}
	var header Header
	if err := decodeSegment(parts[0], &header); err != nil {
		return Claims{}, refuse(ReasonMalformed)
	}
	if header.Algorithm != Algorithm {
		return Claims{}, refuse(ReasonAlgorithm)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return Claims{}, refuse(ReasonSignature)
	}
	var claims Claims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return Claims{}, refuse(ReasonMalformed)
	}
	switch {
	case now.Unix() >= claims.ExpiresAt:
		return Claims{}, refuse(ReasonExpired)
	case claims.Audience != want.Audience:
		return Claims{}, refuse(ReasonAudience)
	case claims.Ocel.Namespace != want.Namespace:
		return Claims{}, refuse(ReasonNamespace)
	case claims.Ocel.Operation != want.Operation:
		return Claims{}, refuse(ReasonOperation)
	case claims.Ocel.Channel != want.Channel:
		return Claims{}, refuse(ReasonChannel)
	}
	return claims, nil
}

func ReadUnverifiedNamespace(raw string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", refuse(ReasonMalformed)
	}
	var claims Claims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return "", refuse(ReasonMalformed)
	}
	return claims.Ocel.Namespace, nil
}

func decodeSegment(segment string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}
