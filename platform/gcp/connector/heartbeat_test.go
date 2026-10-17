package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/connectorserver"
)

const (
	connectorHost    = "ocel-connector-3k2x7lq4ta-ew.a.run.app"
	connectorAccount = "ocel-connector@acme-prod.iam.gserviceaccount.com"
)

type google struct {
	certs  string
	signer jwk.Key
}

func signingKey(t *testing.T, kid string) (private, public jwk.Key) {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private, err = jwk.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	public, err = jwk.Import(&raw.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []jwk.Key{private, public} {
		if err := key.Set(jwk.KeyIDKey, kid); err != nil {
			t.Fatal(err)
		}
		if err := key.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
			t.Fatal(err)
		}
	}
	return private, public
}

func googleServing(t *testing.T) *google {
	t.Helper()
	private, public := signingKey(t, "google-1")
	set := jwk.NewSet()
	if err := set.AddKey(public); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(server.Close)
	return &google{certs: server.URL, signer: private}
}

type idToken struct {
	issuer   string
	audience string
	email    string
	expires  time.Time
}

func scheduled() idToken {
	return idToken{
		issuer:   "https://accounts.google.com",
		audience: "https://" + connectorHost,
		email:    connectorAccount,
		expires:  time.Now().Add(time.Hour),
	}
}

func signed(t *testing.T, signer jwk.Key, claims idToken) string {
	t.Helper()
	token, err := jwt.NewBuilder().
		Issuer(claims.issuer).
		Audience([]string{claims.audience}).
		Subject("104857600000000000000").
		IssuedAt(claims.expires.Add(-time.Hour)).
		Expiration(claims.expires).
		Claim("email", claims.email).
		Claim("email_verified", true).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Sign(token, jwt.WithKey(jwa.RS256(), signer))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func heartbeatRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://"+connectorHost+connectorserver.HeartbeatPath, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestAHeartbeatRequestCarryingCloudSchedulersTokenForTheConnectorsOwnAccountIsAdmitted(t *testing.T) {
	t.Parallel()
	google := googleServing(t)
	caller := scheduledCaller{keys: connectorserver.NewKeyring(google.certs), account: connectorAccount}

	if err := caller.verify(heartbeatRequest(signed(t, google.signer, scheduled()))); err != nil {
		t.Errorf("verify() = %v, want the token Cloud Scheduler mints as the connector's account admitted", err)
	}
}

func TestAHeartbeatRequestWithoutCloudSchedulersTokenIsRefused(t *testing.T) {
	t.Parallel()
	google := googleServing(t)
	stranger, _ := signingKey(t, "google-1")

	for name, token := range map[string]string{
		"no token":                         "",
		"not a jwt":                        "a-stranger",
		"another issuer":                   signed(t, google.signer, func() idToken { c := scheduled(); c.issuer = "https://evil.example.com"; return c }()),
		"another audience":                 signed(t, google.signer, func() idToken { c := scheduled(); c.audience = "https://elsewhere.a.run.app"; return c }()),
		"another account":                  signed(t, google.signer, func() idToken { c := scheduled(); c.email = "someone@acme-prod.iam.gserviceaccount.com"; return c }()),
		"expired":                          signed(t, google.signer, func() idToken { c := scheduled(); c.expires = time.Now().Add(-time.Hour); return c }()),
		"signed by a key Google holds not": signed(t, stranger, scheduled()),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			caller := scheduledCaller{keys: connectorserver.NewKeyring(google.certs), account: connectorAccount}
			if err := caller.verify(heartbeatRequest(token)); err == nil {
				t.Error("verify() = nil, and a stranger may not make the connector beat on their schedule")
			}
		})
	}
}
