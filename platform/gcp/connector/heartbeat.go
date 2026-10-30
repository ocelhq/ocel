package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/endpoint"
)

const (
	googleIssuer   = "https://accounts.google.com"
	googleCertsURL = "https://www.googleapis.com/oauth2/v3/certs"
	tokenSkew      = 30 * time.Second
)

type scheduledCaller struct {
	keys    *endpoint.Keyring
	account string
}

func (s scheduledCaller) verify(r *http.Request) error {
	raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || raw == "" {
		return errors.New("no bearer token")
	}
	message, err := jws.ParseString(raw)
	if err != nil {
		return errors.New("the bearer token is not a signed jwt")
	}
	signatures := message.Signatures()
	if len(signatures) != 1 {
		return errors.New("the bearer token does not have exactly one signature")
	}
	kid, _ := signatures[0].ProtectedHeaders().KeyID()
	set, err := s.keys.Read(r.Context(), kid)
	if err != nil {
		return fmt.Errorf("reach Google's keys: %w", err)
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKeySet(set),
		jwt.WithIssuer(googleIssuer),
		jwt.WithAudience("https://"+r.Host),
		jwt.WithAcceptableSkew(tokenSkew),
	)
	if err != nil {
		return fmt.Errorf("verify the bearer token: %w", err)
	}
	var email string
	if err := token.Get("email", &email); err != nil || email != s.account {
		return fmt.Errorf("the token speaks for %q, not for %s, the account Cloud Scheduler calls this connector as", email, s.account)
	}
	return nil
}
