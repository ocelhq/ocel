package connectorkit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const (
	connectorID    = "conn-shop"
	organizationID = "org-shop"
)

type console struct {
	origin string
	signer jwk.Key
}

func consoleServing(t *testing.T) *console {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	signer, err := jwk.Import(private)
	if err != nil {
		t.Fatalf("Import(private) error = %v", err)
	}
	if err := signer.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatalf("Set(kid) error = %v", err)
	}

	published, err := jwk.Import(public)
	if err != nil {
		t.Fatalf("Import(public) error = %v", err)
	}
	for name, value := range map[string]any{
		jwk.KeyIDKey:     "key-1",
		jwk.AlgorithmKey: jwa.EdDSA(),
	} {
		if err := published.Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatalf("AddKey() error = %v", err)
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

type minted struct {
	audience string
	subject  string
	scope    []string
}

func (c *console) token(t *testing.T, held minted) string {
	t.Helper()

	audience := held.audience
	if audience == "" {
		audience = connectorID
	}
	subject := held.subject
	if subject == "" {
		subject = "org:" + organizationID
	}

	token, err := jwt.NewBuilder().
		Issuer(c.origin).
		Audience([]string{audience}).
		Subject(subject).
		JwtID("one").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(60*time.Second)).
		Claim("act", "user-1").
		Claim("scope", held.scope).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), c.signer))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return string(signed)
}

func connectorServing(t *testing.T, at *console, grants []string, options ...connect.ClientOption) envvarsv1connect.EnvVarsServiceClient {
	t.Helper()

	mux, err := connectorkit.Mux(connectorkit.Spec{
		Version:        "test",
		Vendor:         "fake",
		Target:         "fake/shop",
		Console:        at.origin,
		ConnectorID:    connectorID,
		OrganizationID: organizationID,
		Grants:         grants,
		Vars: providerkit.Vars{
			Records: fake.NewRecords(),
			Sealer:  fake.NewSealer(),
		},
	})
	if err != nil {
		t.Fatalf("Mux() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL, options...)
}

func bearer(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
				request.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, request)
			}
		},
	))
}

func withBearer(t *testing.T, at *console, grants []string, token string) envvarsv1connect.EnvVarsServiceClient {
	t.Helper()

	return connectorServing(t, at, grants, bearer(token))
}

func everything() []string {
	return []string{
		connectorkit.CapabilityEnvVarsRead,
		connectorkit.CapabilityEnvVarsWrite,
		connectorkit.CapabilityEnvVarsReveal,
	}
}

func TestReadPassesUnderAValidToken(t *testing.T) {
	at := consoleServing(t)
	vars := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorkit.CapabilityEnvVarsRead}}))

	if _, err := vars.ListValues(context.Background(), &envvarsv1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	}); err != nil {
		t.Fatalf("ListValues() error = %v", err)
	}
}

func TestAMissingTokenIsUnauthenticated(t *testing.T) {
	at := consoleServing(t)
	vars := connectorServing(t, at, everything())

	_, err := vars.ListValues(context.Background(), &envvarsv1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
}

func TestAnotherAudienceIsUnauthenticated(t *testing.T) {
	at := consoleServing(t)
	vars := withBearer(t, at, everything(), at.token(t, minted{
		audience: "conn-other",
		scope:    []string{connectorkit.CapabilityEnvVarsRead},
	}))

	_, err := vars.ListValues(context.Background(), &envvarsv1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
}

func TestAnotherAccountIsDenied(t *testing.T) {
	at := consoleServing(t)
	vars := withBearer(t, at, everything(), at.token(t, minted{
		subject: "org:other",
		scope:   []string{connectorkit.CapabilityEnvVarsRead},
	}))

	_, err := vars.ListValues(context.Background(), &envvarsv1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: "shop",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestWritingWithoutTheScopeIsDenied(t *testing.T) {
	at := consoleServing(t)
	vars := withBearer(t, at, everything(), at.token(t, minted{scope: []string{connectorkit.CapabilityEnvVarsRead}}))

	_, err := vars.SetValue(context.Background(), &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Value:      "postgres://",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("SetValue() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestRevealingWithoutTheGrantIsDenied(t *testing.T) {
	at := consoleServing(t)
	grants := []string{connectorkit.CapabilityEnvVarsRead, connectorkit.CapabilityEnvVarsWrite}
	vars := withBearer(t, at, grants, at.token(t, minted{scope: everything()}))

	_, err := vars.RevealValues(context.Background(), &envvarsv1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  "shop",
		Cells: []*envvarsv1.Coordinate{{Slug: "shop", Key: "DATABASE_URL"}},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RevealValues() code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}
}

func TestGetValueAsksForRevealOnlyWhenItReveals(t *testing.T) {
	at := consoleServing(t)
	reading := at.token(t, minted{scope: []string{connectorkit.CapabilityEnvVarsRead}})

	plain := withBearer(t, at, everything(), reading)
	if _, err := plain.GetValue(context.Background(), &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
	}); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Fatalf("GetValue() without reveal = %v, want it past the guard", err)
	}

	_, err := plain.GetValue(context.Background(), &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Reveal:     true,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetValue(reveal) code = %v, want %v (err = %v)", connect.CodeOf(err), connect.CodePermissionDenied, err)
	}

	revealing := withBearer(t, at, everything(), at.token(t, minted{scope: everything()}))
	if _, err := revealing.GetValue(context.Background(), &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "DATABASE_URL"},
		Reveal:     true,
	}); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Fatalf("GetValue(reveal) under the reveal scope = %v, want it past the guard", err)
	}
}
