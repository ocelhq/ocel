package consoletoken_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/consoletoken"
)

const audience = "ocel-cloud"

type console struct {
	origin string
	signer jwk.Key
}

func aConsole(t *testing.T) *console {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jwk.Import(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatal(err)
	}
	published, err := jwk.Import(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := published.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := published.Set(jwk.AlgorithmKey, jwa.EdDSA()); err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &console{origin: server.URL, signer: signer}
}

type claims struct {
	audience string
	scope    any
}

func (c *console) sign(t *testing.T, given claims) string {
	t.Helper()

	builder := jwt.NewBuilder().
		Issuer(c.origin).
		Audience([]string{given.audience}).
		Subject("org:shop").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Minute)).
		Claim("act", "user-1")
	if given.scope != nil {
		builder = builder.Claim("scope", given.scope)
	}
	token, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), c.signer))
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + string(signed)
}

func TestATokenTheConsoleSignedReadsAsWhoItSpeaksForAndWhatItMayDo(t *testing.T) {
	t.Parallel()

	at := aConsole(t)
	got, err := consoletoken.NewIssuer(at.origin).Verify(context.Background(), at.sign(t, claims{audience: audience, scope: []string{"cloud.apps.read", "cloud.apps.write"}}), audience)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != "org:shop" || got.Act != "user-1" || !slices.Equal(got.Scopes, []string{"cloud.apps.read", "cloud.apps.write"}) {
		t.Errorf("Verify = %+v, want org:shop acting as user-1 with both scopes", got)
	}
}

func TestATokenTheConsoleDidNotSignForThisAudienceIsUnverified(t *testing.T) {
	t.Parallel()

	at, other := aConsole(t), aConsole(t)
	issuer := consoletoken.NewIssuer(at.origin)
	for name, authorization := range map[string]string{
		"no bearer token":         "",
		"not a jwt":               "Bearer not-a-jwt",
		"another audience":        at.sign(t, claims{audience: "conn-shop", scope: []string{"cloud.apps.read"}}),
		"another console's token": other.sign(t, claims{audience: audience, scope: []string{"cloud.apps.read"}}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := issuer.Verify(context.Background(), authorization, audience); !errors.Is(err, consoletoken.ErrUnverified) {
				t.Errorf("Verify = %v, want ErrUnverified", err)
			}
		})
	}
}

func TestATokenWithoutAListOfScopesIsRefusedForItsScope(t *testing.T) {
	t.Parallel()

	at := aConsole(t)
	issuer := consoletoken.NewIssuer(at.origin + "/")
	for name, scope := range map[string]any{
		"no scope":           nil,
		"a scope of numbers": []int{1, 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := issuer.Verify(context.Background(), at.sign(t, claims{audience: audience, scope: scope}), audience); !errors.Is(err, consoletoken.ErrNoScope) {
				t.Errorf("Verify = %v, want ErrNoScope", err)
			}
		})
	}
}
