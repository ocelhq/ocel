package consoletoken

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/jwks"
)

const (
	keysPath = "/api/auth/jwks"
	skew     = 30 * time.Second
)

var (
	ErrUnverified = errors.New("the bearer token is not one the console signed for this audience")
	ErrNoScope    = errors.New("the token names no list of scopes")
)

type Token struct {
	Subject string
	Act     string
	Scopes  []string
}

type Issuer struct {
	origin string
	keys   *jwks.Keyring
}

func NewIssuer(origin string) *Issuer {
	origin = strings.TrimRight(origin, "/")
	return &Issuer{origin: origin, keys: jwks.NewKeyring(origin + keysPath)}
}

func (i *Issuer) Verify(ctx context.Context, authorization, audience string) (Token, error) {
	raw, found := strings.CutPrefix(authorization, "Bearer ")
	if !found || raw == "" {
		return Token{}, fmt.Errorf("%w: no bearer token", ErrUnverified)
	}
	message, err := jws.ParseString(raw)
	if err != nil {
		return Token{}, fmt.Errorf("%w: the bearer token is not a signed jwt", ErrUnverified)
	}
	signatures := message.Signatures()
	if len(signatures) != 1 {
		return Token{}, fmt.Errorf("%w: the bearer token does not have exactly one signature", ErrUnverified)
	}
	kid, _ := signatures[0].ProtectedHeaders().KeyID()
	set, err := i.keys.Read(ctx, kid)
	if err != nil {
		return Token{}, fmt.Errorf("reach the console keys: %w", err)
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKeySet(set),
		jwt.WithIssuer(i.origin),
		jwt.WithAudience(audience),
		jwt.WithAcceptableSkew(skew),
	)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrUnverified, err)
	}

	subject, _ := token.Subject()
	var act string
	_ = token.Get("act", &act)
	var claimed any
	if err := token.Get("scope", &claimed); err != nil {
		return Token{}, ErrNoScope
	}
	scopes, ok := stringsOf(claimed)
	if !ok {
		return Token{}, ErrNoScope
	}
	return Token{Subject: subject, Act: act, Scopes: scopes}, nil
}

func stringsOf(claimed any) ([]string, bool) {
	switch value := claimed.(type) {
	case []string:
		return value, true
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			name, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, name)
		}
		return out, true
	}
	return nil, false
}
