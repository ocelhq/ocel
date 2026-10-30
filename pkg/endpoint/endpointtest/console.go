package endpointtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/endpoint"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

type Console struct {
	URL    string
	signer jwk.Key
}

func NewConsole(t *testing.T) *Console {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jwk.Import(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Set(jwk.KeyIDKey, "console-key"); err != nil {
		t.Fatal(err)
	}
	published, err := jwk.Import(public)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: "console-key", jwk.AlgorithmKey: jwa.EdDSA()} {
		if err := published.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatal(err)
	}
	serveKeys := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/jwks", serveKeys)
	mux.HandleFunc("GET /oidc/jwks", serveKeys)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &Console{URL: server.URL, signer: signer}
}

func (c *Console) PersonToken(t *testing.T, audience, organizationID, actor string, role scopev1.Role, scopes []scopev1.Scope) string {
	t.Helper()
	return c.sign(t, c.URL, audience, "org:"+organizationID, time.Now().Add(time.Hour), "", map[string]any{
		"act":   actor,
		"role":  scope.ClaimOf(role),
		"scope": scope.ClaimsOf(scopes),
	})
}

func (c *Console) JobToken(t *testing.T, organizationID string, job endpoint.Job) string {
	t.Helper()
	claims := map[string]any{"job_id": job.ID, "runner": job.Runner, "slug": job.Slug}
	if job.DeploymentID != "" {
		claims["deployment_id"] = job.DeploymentID
	}
	if job.Preview != "" {
		claims["preview"] = job.Preview
	}
	subject := "org:" + organizationID + ":tier:" + string(job.Tier) + ":project:" + job.ProjectID
	return c.sign(t, c.URL, endpoint.JobAudience, subject, expiry(job.Expires), job.TokenID, claims)
}

func (c *Console) CredentialToken(t *testing.T, organizationID string, credential endpoint.TargetCredential) string {
	t.Helper()
	subject := "org:" + organizationID + ":target:" + credential.TargetID + ":tier:" + string(credential.Tier) + ":project:" + credential.ProjectID
	return c.sign(t, c.URL+"/oidc", "ocel-target:"+credential.TargetID, subject, expiry(credential.Expires), credential.TokenID, map[string]any{
		"org":     organizationID,
		"target":  credential.TargetID,
		"tier":    string(credential.Tier),
		"project": credential.ProjectID,
		"purpose": credential.Purpose,
	})
}

func expiry(expires time.Time) time.Time {
	if expires.IsZero() {
		return time.Now().Add(time.Hour)
	}
	return expires
}

func (c *Console) sign(t *testing.T, issuer, audience, subject string, expires time.Time, id string, claims map[string]any) string {
	t.Helper()
	if id == "" {
		id = rand.Text()
	}
	builder := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{audience}).
		Subject(subject).
		JwtID(id).
		IssuedAt(time.Now()).
		Expiration(expires)
	for name, value := range claims {
		builder = builder.Claim(name, value)
	}
	token, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), c.signer))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}
